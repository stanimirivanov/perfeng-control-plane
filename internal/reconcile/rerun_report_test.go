package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

type rerunReportFixture struct {
	value                    runrerun.Acceptance
	candidate                run.Artifact
	known                    bool
	lookupErr, errorArtifact error
	legacyCalls              int
}

func (s *rerunReportFixture) LookupRerunExecution(context.Context, string, run.Run) (runrerun.Acceptance, bool, error) {
	return s.value, s.known, s.lookupErr
}
func (s *rerunReportFixture) GetArtifact(context.Context, string, string, string) (run.Artifact, error) {
	return s.candidate, s.errorArtifact
}
func (s *rerunReportFixture) ResolveReportPlan(context.Context, string, run.Run, ReportingInput) (ReportPlan, error) {
	s.legacyCalls++
	return ReportPlan{}, run.ErrForbidden
}
func (s *rerunReportFixture) ResolveContractsVersion(context.Context, string, run.Run) (string, error) {
	s.legacyCalls++
	return "legacy-version", nil
}
func frozenRerunReport(t *testing.T) (*rerunReportFixture, reportApprovalFixture) {
	t.Helper()
	f := newReportApprovalFixture(t)
	pin := f.current.Request.Policy
	f.current.Request = analysisRun().Request
	f.current.Request.Policy = pin
	inputs := runcomparison.Inputs{ContractsVersion: f.manifest.ContractsVersion, Request: f.current.Request, Candidate: f.input.Candidate, Workload: f.record.Workload, PolicyBytes: f.trust.PolicyBytes, PolicyMode: f.trust.PolicyMode, Producer: f.trust.Producer, Baselines: []policy.BaselineResolution{{ID: f.record.ID, Version: f.record.Version, Artifact: &f.record.Artifact}}, BaselineSnapshots: []baseline.Record{f.record}}
	inputs.Candidate.RunID = "perf-20260901-120000-abcdef12"
	inputs.Candidate.ID = "11111111-1111-4111-8111-111111111111"
	actor := serviceaccess.Identity{Issuer: "urn:people", ID: "maya"}
	caller := serviceaccess.Identity{Issuer: "urn:clients", ID: "console"}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	child := f.current
	child.State = run.StateCreated
	child.Revision = 1
	child.CreatedAt = now
	child.UpdatedAt = now
	report := inputs.Candidate
	report.ID = "88888888-8888-4888-8888-888888888888"
	report.Format = "analysis-result/v1"
	v := runrerun.Acceptance{ID: "99999999-9999-4999-8999-999999999999", Intent: runrerun.Intent{SourceRunID: inputs.Candidate.RunID, Key: "rerun-report-test1", ReportID: report.ID, ReportSHA256: report.SHA256, Reason: "investigate", Actor: actor, Caller: caller, ActorKind: "HUMAN"}, Source: serviceaccess.Binding{Service: serviceaccess.Identity{Issuer: "urn:services", ID: "search"}, Creator: actor, CreatorKind: "CI", OwnerPrincipal: "ci-owner", ReviewReference: "review/source"}, Child: child, ChildPrincipal: "child-owner", Report: report, AcceptedAt: now, ExpiresAt: now.Add(24 * time.Hour), Inputs: inputs}
	if err := v.Validate(); err != nil {
		t.Fatal("invalid frozen fixture", err)
	}
	return &rerunReportFixture{value: v, candidate: f.input.Candidate, known: true}, f
}
func TestRerunReportUsesFrozenInputsForJobAndApproval(t *testing.T) {
	store, f := frozenRerunReport(t)
	resolver, err := NewRerunReportPlanResolver(store, store)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.ResolveReportPlan(context.Background(), "child-owner", f.current, f.input)
	if err != nil || plan.Candidate != f.input.Candidate || !reflect.DeepEqual(plan.Baselines, store.value.Inputs.Baselines) || string(plan.PolicyBytes) != string(store.value.Inputs.PolicyBytes) || len(plan.CaptureSHA256) != 64 {
		t.Fatal("rerun report reselected", err)
	}
	version, err := resolver.ResolveContractsVersion(context.Background(), "child-owner", f.current)
	if err != nil || version != store.value.Inputs.ContractsVersion || store.legacyCalls != 0 {
		t.Fatal("schema pin drift", err)
	}
	jobs, err := NewKubernetesReportJobResolver(resolver, reportJobConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, err := jobs.ResolveReportJob(context.Background(), "child-owner", f.current, f.input)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := jobs.ResolveReportJob(context.Background(), "child-owner", f.current, f.input)
	if !reflect.DeepEqual(first, again) {
		t.Fatal("retry job identity drift")
	}
	approver, err := NewReportPlanApprover(resolver, policy.VerdictApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if err = approver.ApproveReportManifest(context.Background(), "child-owner", f.current, f.input, f.manifest); err != nil {
		t.Fatal("approval used a different plan", err)
	}
	plan.PolicyBytes[0] = 'x'
	againPlan, err := resolver.ResolveReportPlan(context.Background(), "child-owner", f.current, f.input)
	if err != nil || string(againPlan.PolicyBytes) != string(store.value.Inputs.PolicyBytes) {
		t.Fatal("returned policy aliases source", err)
	}
}
func TestRerunReportRejectsMissingOrSubstitutedEvidenceWithoutFallback(t *testing.T) {
	for _, mode := range []string{"not-found", "unavailable", "classification-unavailable", "candidate", "request", "owner", "artifact-missing", "policy"} {
		t.Run(mode, func(t *testing.T) {
			store, f := frozenRerunReport(t)
			switch mode {
			case "not-found":
				store.lookupErr = run.ErrNotFound
			case "unavailable":
				store.lookupErr = run.ErrUnavailable
			case "classification-unavailable":
				store.known = false
				store.lookupErr = run.ErrUnavailable
			case "policy":
				store.value.Inputs.PolicyBytes = []byte(`{"untrusted":true}`)
			case "candidate":
				store.candidate.SHA256 = strings.Repeat("f", 64)
			case "request":
				f.current.Request.Candidate.GitSHA = strings.Repeat("f", 40)
			case "owner":
				store.value.ChildPrincipal = "foreign"
			case "artifact-missing":
				store.errorArtifact = run.ErrNotFound
			}
			resolver, _ := NewRerunReportPlanResolver(store, store)
			plan, err := resolver.ResolveReportPlan(context.Background(), "child-owner", f.current, f.input)
			if err == nil || len(plan.PolicyBytes) != 0 || store.legacyCalls != 0 {
				t.Fatal("known rerun fell back or leaked trust", err)
			}
			if mode == "not-found" || mode == "unavailable" || mode == "classification-unavailable" || mode == "request" || mode == "owner" || mode == "policy" {
				version, versionErr := resolver.ResolveContractsVersion(context.Background(), "child-owner", f.current)
				if versionErr == nil || version != "" || store.legacyCalls != 0 {
					t.Fatal("known rerun schema fell back", versionErr)
				}
			}
		})
	}
	store, f := frozenRerunReport(t)
	store.known = false
	store.value = runrerun.Acceptance{}
	resolver, _ := NewRerunReportPlanResolver(store, store)
	if _, err := resolver.ResolveReportPlan(context.Background(), "ordinary", f.current, f.input); !errors.Is(err, run.ErrForbidden) || store.legacyCalls != 1 {
		t.Fatal("ordinary report behavior changed", err)
	}
	version, err := resolver.ResolveContractsVersion(context.Background(), "ordinary", f.current)
	if err != nil || version != "legacy-version" {
		t.Fatal("ordinary schema behavior changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = resolver.ResolveReportPlan(ctx, "ordinary", f.current, f.input); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}
