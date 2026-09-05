package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ httpapi.Approve = (&ReportPolicyRegistry{}).ApproveRun

func executionTemplate(image string) *batchv1.Job {
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false

	return &batchv1.Job{
		Spec: batchv1.JobSpec{
			BackoffLimit:          &zero,
			Completions:           &one,
			Parallelism:           &one,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					Containers: []corev1.Container{{
						Name: "runner", Image: image, Args: []string{"run"},
					}},
				},
			},
		},
	}
}

func reportPolicyFixture(t *testing.T) (ReportPolicyEntry, run.Run, reconcile.ReportingInput) {
	t.Helper()
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	policyBytes = []byte(strings.Replace(
		string(policyBytes), `"name": "search-browser"`, `"name": "search-policy"`, 1,
	))
	digest := sha256.Sum256(policyBytes)
	catalogue := run.Reference{
		ID: "application-tests", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
	}
	environment := baseline.Environment{
		Identity: rawresult.Identity{
			ID: "local-kind", Version: "1.0.0", SHA256: strings.Repeat("d", 64),
		},
		Fingerprint: strings.Repeat("e", 64),
	}
	seed := int64(42)
	entry := ReportPolicyEntry{
		PolicyBytes: policyBytes, TestID: "search-browser", ContractsVersion: "0.8.0",
		Catalogue: catalogue, Profile: "smoke",
		RawProducer: rawresult.Producer{
			Name: "perfeng-k6", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-k6@sha256:" + strings.Repeat("0", 64),
		},
		NormalizerProducer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("f", 64),
		},
		ReportProducer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("f", 64),
		},
		Workload: rawresult.Identity{
			ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("b", 64),
		},
		Environment: environment,
		Dataset: baseline.Dataset{
			Kind: "versioned", ID: "search-data", Version: "1.0.0",
			SHA256: strings.Repeat("c", 64), Seed: &seed,
		},
		CandidateImages: []string{
			"ghcr.io/example/search@sha256:" + strings.Repeat("2", 64),
		},
		Principals: []string{"alice"},
	}
	entry.ExecutionTemplate = executionTemplate(entry.RawProducer.Image)
	current := run.Run{
		ID: "perf-20260905-120000-12345678", State: run.StateReporting, Revision: 7,
		Request: run.Request{
			TestSuite: "search-browser", Catalogue: catalogue, Profile: "smoke",
			Candidate: run.Candidate{
				GitSHA: strings.Repeat("1", 40),
				Image:  "ghcr.io/example/search@sha256:" + strings.Repeat("2", 64),
			},
			Environment: run.Reference{
				ID: environment.ID, Version: environment.Version, SHA256: environment.SHA256,
			},
			Policy: run.Reference{
				ID: "search-policy", Version: "1.0.0",
				SHA256: hex.EncodeToString(digest[:]),
			},
		},
	}
	input := reconcile.ReportingInput{Candidate: run.Artifact{
		ID: "11111111-1111-4111-8111-111111111111", RunID: current.ID,
		Kind: "normalized", URI: "s3://perfeng/runs/" + current.ID + "/normalized.json",
		SHA256: strings.Repeat("3", 64), SizeBytes: 100,
		MediaType: "application/json", Format: "normalized-result/v1",
	}}
	if current.Request.Validate() != nil || input.Candidate.Validate() != nil {
		t.Fatal("invalid registry test fixture")
	}

	return entry, current, input
}

func TestReportPolicyRegistryApprovesExactRunRequest(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveRun(context.Background(), "alice", current.Request); err != nil {
		t.Fatal(err)
	}
}

func TestReportPolicyRegistryRejectsUnapprovedRunRequest(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal string
		mutate    func(*run.Request)
	}{
		{name: "principal", principal: "bob"},
		{name: "test", principal: "alice", mutate: func(request *run.Request) {
			request.TestSuite = "other-test"
		}},
		{name: "catalogue", principal: "alice", mutate: func(request *run.Request) {
			request.Catalogue.Version = "2.0.0"
		}},
		{name: "profile", principal: "alice", mutate: func(request *run.Request) {
			request.Profile = "regression"
		}},
		{name: "environment", principal: "alice", mutate: func(request *run.Request) {
			request.Environment.SHA256 = strings.Repeat("6", 64)
		}},
		{name: "policy", principal: "alice", mutate: func(request *run.Request) {
			request.Policy.SHA256 = strings.Repeat("7", 64)
		}},
		{name: "candidate", principal: "alice", mutate: func(request *run.Request) {
			request.Candidate.Image = "ghcr.io/example/search@sha256:" + strings.Repeat("8", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&current.Request)
			}
			if err := registry.ApproveRun(
				context.Background(), test.principal, current.Request,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesRunApprovalContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveRun(
		context.Background(), "", current.Request,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("principal error = %v", err)
	}
	invalid := current.Request
	invalid.Candidate.Image = "latest"
	if err := registry.ApproveRun(
		context.Background(), "alice", invalid,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("request error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.ApproveRun(ctx, "alice", current.Request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if err := nilRegistry.ApproveRun(
		context.Background(), "alice", current.Request,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}

func rawManifestFixture(
	entry ReportPolicyEntry,
	current run.Run,
) rawresult.Manifest {
	return rawresult.Manifest{
		SchemaVersion: 1, Kind: "RawResult", ContractsVersion: entry.ContractsVersion,
		RunID: current.ID, TestID: current.Request.TestSuite, Workload: entry.Workload,
		Producer: entry.RawProducer,
		MeasurementWindow: rawresult.Window{
			Start: "2026-09-05T12:00:00Z", End: "2026-09-05T12:01:00Z",
		},
		CreatedAt: "2026-09-05T12:01:01Z",
		Artifacts: []run.Artifact{{
			ID: "22222222-2222-4222-8222-222222222222", RunID: current.ID,
			Kind: "raw", URI: "s3://perfeng/runs/" + current.ID + "/summary.json",
			SHA256: strings.Repeat("a", 64), SizeBytes: 100,
			MediaType: "application/json", Format: "k6-summary/v1",
		}},
	}
}

func normalizedManifestFixture(
	entry ReportPolicyEntry,
	current run.Run,
) (reconcile.AnalysisInput, normalizedresult.Manifest) {
	rawManifest := rawManifestFixture(entry, current)
	input := reconcile.AnalysisInput{
		Manifest: run.Artifact{
			ID: "33333333-3333-4333-8333-333333333333", RunID: current.ID,
			Kind: "raw", URI: "s3://perfeng/runs/" + current.ID + "/raw-result.json",
			SHA256: strings.Repeat("b", 64), SizeBytes: 200,
			MediaType: "application/json", Format: "raw-result/v1",
		},
		Sources: append([]run.Artifact(nil), rawManifest.Artifacts...),
	}
	manifest := normalizedresult.Manifest{
		SchemaVersion: 1, Kind: "NormalizedResult", ContractsVersion: entry.ContractsVersion,
		RunID: current.ID, TestID: current.Request.TestSuite, Workload: entry.Workload,
		Producer: entry.NormalizerProducer, MeasurementWindow: rawManifest.MeasurementWindow,
		CreatedAt:       "2026-09-05T12:01:02Z",
		SourceArtifacts: append([]run.Artifact(nil), input.Sources...),
		Results: []normalizedresult.Result{{
			SchemaVersion: 2, RunID: current.ID,
			Metric: normalizedresult.Metric{
				Name: "http.request.duration", Direction: "lower-is-better",
			},
			Distribution: normalizedresult.Distribution{},
		}},
	}

	return input, manifest
}

func TestReportPolicyRegistryApprovesRawManifestProvenance(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateCollecting
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveRawManifest(
		context.Background(), "alice", current, rawManifestFixture(entry, current),
	); err != nil {
		t.Fatal(err)
	}
}

func TestReportPolicyRegistryApprovesNormalizedManifestProvenance(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateAnalyzing
	input, manifest := normalizedManifestFixture(entry, current)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveNormalizedManifest(
		context.Background(), "alice", current, input, manifest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReportPolicyRegistryRejectsUnapprovedNormalizedManifest(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*run.Run, *reconcile.AnalysisInput, *normalizedresult.Manifest)
	}{
		{name: "context", mutate: func(current *run.Run, _ *reconcile.AnalysisInput, _ *normalizedresult.Manifest) {
			current.Request.Policy.Version = "2.0.0"
		}},
		{name: "contracts", mutate: func(_ *run.Run, _ *reconcile.AnalysisInput, manifest *normalizedresult.Manifest) {
			manifest.ContractsVersion = "0.9.0"
		}},
		{name: "test", mutate: func(_ *run.Run, _ *reconcile.AnalysisInput, manifest *normalizedresult.Manifest) {
			manifest.TestID = "other-test"
		}},
		{name: "workload", mutate: func(_ *run.Run, _ *reconcile.AnalysisInput, manifest *normalizedresult.Manifest) {
			manifest.Workload.ID = "other-workload"
		}},
		{name: "producer", mutate: func(_ *run.Run, _ *reconcile.AnalysisInput, manifest *normalizedresult.Manifest) {
			manifest.Producer.Name = "other-normalizer"
		}},
		{name: "sources", mutate: func(_ *run.Run, _ *reconcile.AnalysisInput, manifest *normalizedresult.Manifest) {
			manifest.SourceArtifacts[0].SHA256 = strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			current.State = run.StateAnalyzing
			input, manifest := normalizedManifestFixture(entry, current)
			test.mutate(&current, &input, &manifest)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.ApproveNormalizedManifest(
				context.Background(), "alice", current, input, manifest,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesNormalizedManifestContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateAnalyzing
	input, manifest := normalizedManifestFixture(entry, current)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     reconcile.AnalysisInput
		manifest  normalizedresult.Manifest
	}{
		{name: "principal", state: run.StateAnalyzing, input: input, manifest: manifest},
		{name: "state", principal: "alice", state: run.StateRunning, input: input, manifest: manifest},
		{name: "input", principal: "alice", state: run.StateAnalyzing, manifest: manifest},
		{name: "manifest", principal: "alice", state: run.StateAnalyzing, input: input},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if err := registry.ApproveNormalizedManifest(
				context.Background(), test.principal, candidate, test.input, test.manifest,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.ApproveNormalizedManifest(
		ctx, "alice", current, input, manifest,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if err := nilRegistry.ApproveNormalizedManifest(
		context.Background(), "alice", current, input, manifest,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}

func TestReportPolicyRegistryResolvesIsolatedExecutionTemplate(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	entry.ExecutionTemplate.Spec.Template.Spec.Containers[0].Args[0] = "changed-after-construction"

	for _, state := range []run.State{run.StateValidating, run.StateProvisioning} {
		current.State = state
		template, err := registry.ResolveJob(context.Background(), "alice", current)
		if err != nil {
			t.Fatal(err)
		}
		if template.Spec.Template.Spec.Containers[0].Args[0] != "run" {
			t.Fatal("registry template changed through the constructor input")
		}
		template.Spec.Template.Spec.Containers[0].Args[0] = "changed-result"
	}

	current.State = run.StateProvisioning
	template, err := registry.ResolveJob(context.Background(), "alice", current)
	if err != nil {
		t.Fatal(err)
	}
	if template.Spec.Template.Spec.Containers[0].Args[0] != "run" {
		t.Fatal("registry template changed through a resolved copy")
	}
}

func TestReportPolicyRegistryRejectsUnapprovedExecutionTemplateContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*run.Run)
	}{
		{name: "context", mutate: func(current *run.Run) {
			current.Request.Policy.Version = "2.0.0"
		}},
		{name: "candidate", mutate: func(current *run.Run) {
			current.Request.Candidate.Image = "ghcr.io/example/search@sha256:" + strings.Repeat("3", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			current.State = run.StateProvisioning
			test.mutate(&current)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.ResolveJob(
				context.Background(), "alice", current,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesExecutionTemplateContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateValidating
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
	}{
		{name: "principal", state: run.StateValidating},
		{name: "state", principal: "alice", state: run.StateRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if _, err := registry.ResolveJob(
				context.Background(), test.principal, candidate,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveJob(ctx, "alice", current); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if _, err := nilRegistry.ResolveJob(
		context.Background(), "alice", current,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}

func TestReportPolicyRegistryRejectsUnapprovedRawManifest(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*run.Run, *rawresult.Manifest)
	}{
		{name: "context", mutate: func(current *run.Run, _ *rawresult.Manifest) {
			current.Request.Policy.Version = "2.0.0"
		}},
		{name: "contracts", mutate: func(_ *run.Run, manifest *rawresult.Manifest) {
			manifest.ContractsVersion = "0.9.0"
		}},
		{name: "test", mutate: func(_ *run.Run, manifest *rawresult.Manifest) {
			manifest.TestID = "other-test"
		}},
		{name: "workload", mutate: func(_ *run.Run, manifest *rawresult.Manifest) {
			manifest.Workload.ID = "other-workload"
		}},
		{name: "producer", mutate: func(_ *run.Run, manifest *rawresult.Manifest) {
			manifest.Producer.Name = "other-runner"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			current.State = run.StateCollecting
			manifest := rawManifestFixture(entry, current)
			test.mutate(&current, &manifest)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.ApproveRawManifest(
				context.Background(), "alice", current, manifest,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesRawManifestContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateCollecting
	manifest := rawManifestFixture(entry, current)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		manifest  rawresult.Manifest
	}{
		{name: "principal", state: run.StateCollecting, manifest: manifest},
		{name: "state", principal: "alice", state: run.StateRunning, manifest: manifest},
		{name: "manifest", principal: "alice", state: run.StateCollecting},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if err := registry.ApproveRawManifest(
				context.Background(), test.principal, candidate, test.manifest,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.ApproveRawManifest(
		ctx, "alice", current, manifest,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if err := nilRegistry.ApproveRawManifest(
		context.Background(), "alice", current, manifest,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}

func TestReportPolicyRegistryResolvesExactTrust(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	trust, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if string(trust.PolicyBytes) != string(entry.PolicyBytes) || trust.PolicyMode != "inform" ||
		trust.Producer != entry.ReportProducer || len(trust.Baselines) != 1 {
		t.Fatalf("trust = %#v", trust)
	}
	selection := trust.Baselines[0]
	if selection.ID != "approved-search-browser" || selection.Version != "1.0.0" ||
		selection.TestID != current.Request.TestSuite || selection.Workload != entry.Workload ||
		selection.Environment != entry.Environment || selection.Validate() != nil ||
		selection.Dataset.Seed == nil || *selection.Dataset.Seed != 42 {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestReportPolicyRegistryDoesNotReapplyCandidateAdmission(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	current.Request.Candidate.Image = "ghcr.io/example/search@sha256:" + strings.Repeat("9", 64)
	if _, err := registry.ResolveReportTrust(
		context.Background(), "alice", current, input,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReportPolicyRegistryOwnsEntryAndResultData(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	entry.PolicyBytes[0] = 'x'
	*entry.Dataset.Seed = 7

	first, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	first.PolicyBytes[0] = 'y'
	*first.Baselines[0].Dataset.Seed = 8
	second, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.PolicyBytes[0] != '{' || *second.Baselines[0].Dataset.Seed != 42 {
		t.Fatal("registry data was mutated through caller-owned storage")
	}
}

func TestReportPolicyRegistryRejectsUnapprovedContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal string
		mutate    func(*run.Run)
	}{
		{name: "principal", principal: "bob"},
		{name: "test", principal: "alice", mutate: func(current *run.Run) {
			current.Request.TestSuite = "other-test"
		}},
		{name: "catalogue", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Catalogue.SHA256 = strings.Repeat("4", 64)
		}},
		{name: "profile", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Profile = "regression"
		}},
		{name: "environment", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Environment.SHA256 = strings.Repeat("5", 64)
		}},
		{name: "policy", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Policy.Version = "2.0.0"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, input := reportPolicyFixture(t)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&current)
			}
			if _, err := registry.ResolveReportTrust(
				context.Background(), test.principal, current, input,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesEntries(t *testing.T) {
	if _, err := NewReportPolicyRegistry(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("empty error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ReportPolicyEntry)
	}{
		{name: "policy", mutate: func(entry *ReportPolicyEntry) { entry.PolicyBytes = []byte(`{}`) }},
		{name: "test", mutate: func(entry *ReportPolicyEntry) { entry.TestID = "Invalid" }},
		{name: "catalogue", mutate: func(entry *ReportPolicyEntry) {
			entry.Catalogue.SHA256 = "invalid"
		}},
		{name: "profile", mutate: func(entry *ReportPolicyEntry) { entry.Profile = "Invalid" }},
		{name: "contracts", mutate: func(entry *ReportPolicyEntry) {
			entry.ContractsVersion = "latest"
		}},
		{name: "raw producer", mutate: func(entry *ReportPolicyEntry) {
			entry.RawProducer.Image = "latest"
		}},
		{name: "normalizer producer", mutate: func(entry *ReportPolicyEntry) {
			entry.NormalizerProducer.Image = "latest"
		}},
		{name: "report producer", mutate: func(entry *ReportPolicyEntry) {
			entry.ReportProducer.Image = "latest"
		}},
		{name: "execution template", mutate: func(entry *ReportPolicyEntry) {
			entry.ExecutionTemplate = nil
		}},
		{name: "execution policy", mutate: func(entry *ReportPolicyEntry) {
			*entry.ExecutionTemplate.Spec.BackoffLimit = 1
		}},
		{name: "execution producer", mutate: func(entry *ReportPolicyEntry) {
			entry.ExecutionTemplate.Spec.Template.Spec.Containers[0].Image =
				"ghcr.io/example/other@sha256:" + strings.Repeat("4", 64)
		}},
		{name: "workload", mutate: func(entry *ReportPolicyEntry) { entry.Workload.SHA256 = "invalid" }},
		{name: "environment", mutate: func(entry *ReportPolicyEntry) {
			entry.Environment.Fingerprint = "invalid"
		}},
		{name: "dataset", mutate: func(entry *ReportPolicyEntry) { *entry.Dataset.Seed = -1 }},
		{name: "candidate images", mutate: func(entry *ReportPolicyEntry) {
			entry.CandidateImages = nil
		}},
		{name: "candidate image", mutate: func(entry *ReportPolicyEntry) {
			entry.CandidateImages = []string{"latest"}
		}},
		{name: "duplicate candidate image", mutate: func(entry *ReportPolicyEntry) {
			entry.CandidateImages = append(entry.CandidateImages, entry.CandidateImages[0])
		}},
		{name: "principals", mutate: func(entry *ReportPolicyEntry) { entry.Principals = nil }},
		{name: "blank principal", mutate: func(entry *ReportPolicyEntry) {
			entry.Principals = []string{" "}
		}},
		{name: "duplicate principal", mutate: func(entry *ReportPolicyEntry) {
			entry.Principals = []string{"alice", "alice"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, _, _ := reportPolicyFixture(t)
			test.mutate(&entry)
			if _, err := NewReportPolicyRegistry(
				[]ReportPolicyEntry{entry},
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	entry, _, _ := reportPolicyFixture(t)
	if _, err := NewReportPolicyRegistry(
		[]ReportPolicyEntry{entry, entry},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("duplicate entry error = %v", err)
	}
}

func TestReportPolicyRegistryValidatesResolutionContext(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     reconcile.ReportingInput
	}{
		{name: "principal", state: run.StateReporting, input: input},
		{name: "state", principal: "alice", state: run.StateRunning, input: input},
		{name: "candidate", principal: "alice", state: run.StateReporting},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if _, err := registry.ResolveReportTrust(
				context.Background(), test.principal, candidate, test.input,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveReportTrust(
		ctx, "alice", current, input,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if _, err := nilRegistry.ResolveReportTrust(
		context.Background(), "alice", current, input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}
