package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

// Synthetic manifests and in-memory object bytes qualify adapter composition,
// not real k6 execution, normalization, Kubernetes or object-store transfer.
// Lifecycle states are advanced by trusted test setup, not by live executors.
type rerunEvidenceQualification struct {
	r          *Repository
	dsn        string
	accepted   runrerun.Acceptance
	current    run.Run
	entry      registry.ReportPolicyEntry
	objects    map[run.Artifact][]byte
	input      reconcile.AnalysisInput
	normalized run.Artifact
}

func newRerunEvidenceQualification(t *testing.T) *rerunEvidenceQualification {
	t.Helper()
	r, dsn, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	request := accepted.Child.Request
	inputs := accepted.Inputs
	record := inputs.BaselineSnapshots[0]
	entry := registry.ReportPolicyEntry{TestID: request.TestSuite, ContractsVersion: inputs.ContractsVersion, Catalogue: request.Catalogue, Profile: request.Profile, PolicyBytes: inputs.PolicyBytes,
		RawProducer: rawresult.Producer{Name: "k6", Version: "1.0.0", Image: rerunTemplate().Spec.Template.Spec.Containers[0].Image}, NormalizerProducer: inputs.Producer.Clone(), ReportProducer: inputs.Producer.Clone(),
		Normalization: reconcile.NormalizationSpec{SourceFormat: reconcile.K6SummaryFormat, Layout: reconcile.K6TypedLayout}, ExecutionTemplate: rerunTemplate(), Workload: inputs.Workload, Environment: record.Environment, Dataset: record.Dataset, CandidateImages: []string{request.Candidate.Image}, Principals: []string{accepted.Source.OwnerPrincipal}}
	reviewed, err := registry.NewReportPolicyRegistry([]registry.ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal("synthetic registry fixture invalid", err)
	}
	claim := validatingRerunClaim(t, r, accepted)
	jobs, err := reconcile.NewRerunJobResolver(r, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.ResolveClaimJob(testContext, claim); err != nil {
		t.Fatal("synthetic validation seal refused", err)
	}
	current := claim.Run
	for _, state := range []run.State{run.StateProvisioning, run.StateRunning, run.StateCollecting} {
		current, err = r.Advance(testContext, accepted.ChildPrincipal, current.ID, current.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	q := &rerunEvidenceQualification{r: r, dsn: dsn, accepted: accepted, current: current, entry: entry, objects: map[run.Artifact][]byte{}}
	source := q.put(t, "aa100000-0000-4000-8000-000000000001", "synthetic-summary.json", "raw", reconcile.K6SummaryFormat, map[string]any{"metrics": map[string]any{}})
	window := rawresult.Window{Start: "2026-11-13T12:00:00Z", End: "2026-11-13T12:01:00Z"}
	manifest := rawresult.Manifest{SchemaVersion: 1, Kind: "RawResult", ContractsVersion: inputs.ContractsVersion, RunID: current.ID, TestID: request.TestSuite, Workload: inputs.Workload, Producer: entry.RawProducer, MeasurementWindow: window, CreatedAt: "2026-11-13T12:01:01Z", Artifacts: []run.Artifact{source}}
	raw := q.put(t, "bb100000-0000-4000-8000-000000000001", "synthetic-raw-result.json", "raw", "raw-result/v1", manifest)
	q.input = reconcile.AnalysisInput{Manifest: raw, Sources: []run.Artifact{source}}
	normalized := normalizedresult.Manifest{SchemaVersion: 1, Kind: "NormalizedResult", ContractsVersion: inputs.ContractsVersion, RunID: current.ID, TestID: request.TestSuite, Workload: inputs.Workload, Producer: entry.NormalizerProducer, MeasurementWindow: window, CreatedAt: "2026-11-13T12:01:02Z", SourceArtifacts: []run.Artifact{source}, Results: []normalizedresult.Result{{SchemaVersion: 2, RunID: current.ID, Metric: normalizedresult.Metric{Name: "api.http.duration", Direction: "lower-is-better"}, Distribution: normalizedresult.Distribution{}}}}
	q.normalized = q.put(t, "cc100000-0000-4000-8000-000000000001", "synthetic-normalized.json", "normalized", "normalized-result/v1", normalized)
	return q
}
func (q *rerunEvidenceQualification) put(t *testing.T, id, key, kind, format string, value any) run.Artifact {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	ref := run.Artifact{ID: id, RunID: q.current.ID, Kind: kind, Format: format, MediaType: "application/json", URI: "s3://synthetic-evidence/runs/" + q.current.ID + "/" + key, SizeBytes: int64(len(data)), SHA256: run.BenchmarkDigest(data)}
	q.objects[ref] = data
	return ref
}
func (q *rerunEvidenceQualification) Read(ctx context.Context, ref run.Artifact) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, exists := q.objects[ref]
	if !exists || int64(len(data)) != ref.SizeBytes || run.BenchmarkDigest(data) != ref.SHA256 {
		return nil, run.ErrValidation
	}
	return append([]byte(nil), data...), nil
}
func (q *rerunEvidenceQualification) ResolveRawManifest(context.Context, string, run.Run) (run.Artifact, error) {
	return q.input.Manifest, nil
}
func (q *rerunEvidenceQualification) ResolveNormalizedManifest(context.Context, string, run.Run) (run.Artifact, error) {
	return q.normalized, nil
}
func (q *rerunEvidenceQualification) adapters(t *testing.T, r *Repository, entry registry.ReportPolicyEntry) (*reconcile.VerifiedRawCollector, *reconcile.VerifiedNormalizedCollector, *reconcile.RerunNormalizationResolver) {
	t.Helper()
	reviewed, err := registry.NewReportPolicyRegistry([]registry.ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	rawApproval, err := reconcile.NewRerunRawApprover(r, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	normalization, err := reconcile.NewRerunNormalizationResolver(r, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := reconcile.NewRerunReportPlanResolver(r, &unexpectedRerunLegacy{t: t})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := reconcile.NewVerifiedRawCollector(q, q, rawApproval, versions)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := reconcile.NewVerifiedNormalizedCollector(q, q, normalization, versions)
	if err != nil {
		t.Fatal(err)
	}
	return raw, normalized, normalization
}
