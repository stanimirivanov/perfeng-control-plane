package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
)

type retainedInputsFixture struct {
	inputs      *runcomparison.Inputs
	commitError error
	enrolled    bool
}

func (store *retainedInputsFixture) GetReportInputs(context.Context, string, string) (runcomparison.Inputs, error) {
	if store.inputs == nil {
		return runcomparison.Inputs{}, run.ErrNotFound
	}
	return store.inputs.Clone(), nil
}
func (store *retainedInputsFixture) RetainReportInputs(_ context.Context, _ string, _ run.Run, inputs runcomparison.Inputs) (runcomparison.Inputs, error) {
	if !store.enrolled {
		return runcomparison.Inputs{}, runcomparison.ErrNotEnrolled
	}
	stored := inputs.Clone()
	store.inputs = &stored
	if store.commitError != nil {
		return runcomparison.Inputs{}, store.commitError
	}
	return stored.Clone(), nil
}
func TestRetainedReportPlansReplayBeforeRegistryAndApproval(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	selectedPolicy := fixture.current.Request.Policy
	fixture.current.Request = analysisRun().Request
	fixture.current.Request.Policy = selectedPolicy
	store := &retainedInputsFixture{enrolled: true, commitError: run.ErrUnavailable}
	calls := 0
	original := ReportPlan{ContractsVersion: fixture.manifest.ContractsVersion, Workload: fixture.record.Workload, Candidate: fixture.input.Candidate, PolicyBytes: fixture.trust.PolicyBytes, PolicyMode: fixture.trust.PolicyMode, Producer: fixture.trust.Producer,
		Baselines: []policy.BaselineResolution{{ID: fixture.record.ID, Version: fixture.record.Version, Artifact: &fixture.record.Artifact}}, BaselineSnapshots: []baseline.Record{fixture.record}}
	selected := resolveReportPlanFunc(func(context.Context, string, run.Run, ReportingInput) (ReportPlan, error) {
		calls++
		if calls > 1 {
			return ReportPlan{}, run.ErrForbidden
		}
		return original.Clone(), nil
	})
	resolver, err := NewRetainedReportPlanResolver(store, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveReportPlan(context.Background(), "principal-a", fixture.current, fixture.input); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("uncertain commit hidden", err)
	}
	got, err := resolver.ResolveReportPlan(context.Background(), "principal-a", fixture.current, fixture.input)
	if err != nil || calls != 1 || !reflect.DeepEqual(got.Baselines, original.Baselines) || got.CaptureSHA256 == "" {
		t.Fatal("retry reselected inputs", err, calls)
	}
	got.PolicyBytes[0] = 'x'
	got.BaselineSnapshots[0].ID = "mutated"
	again, err := resolver.ResolveReportPlan(context.Background(), "principal-a", fixture.current, fixture.input)
	if err != nil || string(again.PolicyBytes) != string(original.PolicyBytes) || again.BaselineSnapshots[0].ID != fixture.record.ID {
		t.Fatal("retained inputs alias caller", err)
	}
	changed := fixture.input
	changed.Candidate.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := resolver.ResolveReportPlan(context.Background(), "principal-a", fixture.current, changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal("changed candidate replay accepted", err)
	}
	approver, err := NewReportPlanApprover(resolver, policy.VerdictApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.ApproveReportManifest(context.Background(), "principal-a", fixture.current, fixture.input, fixture.manifest); err != nil || calls != 1 {
		t.Fatal("attestation reread current approval", err, calls)
	}
}

func TestCapturedReportJobAndSchemaUseTheRetainedInputs(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	selectedPolicy := fixture.current.Request.Policy
	fixture.current.Request = analysisRun().Request
	fixture.current.Request.Policy = selectedPolicy
	store := &retainedInputsFixture{enrolled: true}
	plan := ReportPlan{ContractsVersion: fixture.manifest.ContractsVersion, Workload: fixture.record.Workload, Candidate: fixture.input.Candidate, PolicyBytes: fixture.trust.PolicyBytes, PolicyMode: fixture.trust.PolicyMode, Producer: fixture.trust.Producer, Baselines: []policy.BaselineResolution{{ID: fixture.record.ID, Version: fixture.record.Version}}}
	calls := 0
	selected := resolveReportPlanFunc(func(context.Context, string, run.Run, ReportingInput) (ReportPlan, error) { calls++; return plan, nil })
	retained, err := NewRetainedReportPlanResolver(store, selected)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := NewKubernetesReportJobResolver(retained, reportJobConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, err := jobs.ResolveReportJob(context.Background(), "principal-a", fixture.current, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	marker := first.Spec.Template.Annotations["perfeng.dev/report-inputs-sha256"]
	if len(marker) != 64 {
		t.Fatal("report job lacks retained-input identity")
	}
	// A newly available baseline must not turn the retained missing reference into approval.
	plan.Baselines[0].Artifact = &fixture.record.Artifact
	plan.BaselineSnapshots = []baseline.Record{fixture.record}
	again, err := jobs.ResolveReportJob(context.Background(), "principal-a", fixture.current, fixture.input)
	if err != nil || calls != 1 || !reflect.DeepEqual(again, first) {
		t.Fatal("retry adopted newly approved baseline", err, calls)
	}
	version, err := retained.ResolveContractsVersion(context.Background(), "principal-a", fixture.current)
	if err != nil || version != fixture.manifest.ContractsVersion {
		t.Fatal("report schema reselected", err)
	}
}
