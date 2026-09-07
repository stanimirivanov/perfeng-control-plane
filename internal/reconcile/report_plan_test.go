package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestTrustedReportPlanResolverBindsExactApprovedInputs(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	resolutions := 0
	planner, err := NewTrustedReportPlanResolver(
		&baselineApprovalStore{resolve: func(
			ctx context.Context,
			principal string,
			selection baseline.Selection,
		) (baseline.Record, bool, error) {
			resolutions++
			if ctx.Err() != nil || principal != "principal-a" ||
				!reflect.DeepEqual(selection, fixture.selection) {
				t.Fatal("baseline repository received changed selection context")
			}

			return fixture.record, true, nil
		}},
		reportTrustResolverFunc(func(
			ctx context.Context,
			principal string,
			current run.Run,
			input ReportingInput,
		) (ReportTrust, error) {
			if ctx.Err() != nil || principal != "principal-a" ||
				!reflect.DeepEqual(current, fixture.current) || input != fixture.input {
				t.Fatal("trust resolver received changed reporting context")
			}

			return fixture.trust, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := planner.ResolveReportPlan(
		context.Background(), "principal-a", fixture.current, fixture.input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Validate(fixture.current.ID) != nil || plan.Candidate != fixture.input.Candidate ||
		string(plan.PolicyBytes) != string(fixture.trust.PolicyBytes) ||
		plan.PolicyMode != fixture.trust.PolicyMode || !plan.Producer.Equal(fixture.trust.Producer) ||
		len(plan.Baselines) != 1 || plan.Baselines[0].ID != fixture.selection.ID ||
		plan.Baselines[0].Version != fixture.selection.Version ||
		plan.Baselines[0].Artifact == nil || *plan.Baselines[0].Artifact != fixture.record.Artifact {
		t.Fatal("planner changed approved report inputs")
	}
	if resolutions != 1 {
		t.Fatalf("baseline resolutions = %d", resolutions)
	}

	plan.PolicyBytes[0] = 'x'
	plan.Baselines[0].Artifact.ID = "changed"
	again, err := planner.ResolveReportPlan(
		context.Background(), "principal-a", fixture.current, fixture.input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.PolicyBytes) != string(fixture.trust.PolicyBytes) ||
		again.Baselines[0].Artifact == nil || *again.Baselines[0].Artifact != fixture.record.Artifact {
		t.Fatal("returned plan shared mutable trust data")
	}
}

func TestTrustedReportPlanResolverRepresentsUnavailableBaseline(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	planner, err := NewTrustedReportPlanResolver(
		&baselineApprovalStore{resolve: func(
			context.Context, string, baseline.Selection,
		) (baseline.Record, bool, error) {
			return baseline.Record{}, false, nil
		}},
		reportTrustResolverFunc(func(
			context.Context, string, run.Run, ReportingInput,
		) (ReportTrust, error) {
			return fixture.trust, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := planner.ResolveReportPlan(
		context.Background(), "principal-a", fixture.current, fixture.input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Baselines) != 1 || plan.Baselines[0].ID != fixture.selection.ID ||
		plan.Baselines[0].Version != fixture.selection.Version || plan.Baselines[0].Artifact != nil {
		t.Fatal("missing approved baseline was not preserved as an unresolved policy reference")
	}
}

func TestReportPlanRejectsInvalidBindings(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	plan := ReportPlan{
		Candidate: fixture.input.Candidate, PolicyBytes: fixture.trust.PolicyBytes,
		PolicyMode: fixture.trust.PolicyMode, Producer: fixture.trust.Producer,
		Baselines: []policy.BaselineResolution{{
			ID: fixture.selection.ID, Version: fixture.selection.Version,
			Artifact: &fixture.record.Artifact,
		}},
	}
	if err := plan.Validate(fixture.current.ID); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*ReportPlan){
		"candidate": func(value *ReportPlan) { value.Candidate.RunID = "other-run" },
		"policy":    func(value *ReportPlan) { value.PolicyBytes[0] = 'x' },
		"mode":      func(value *ReportPlan) { value.PolicyMode = "enforce" },
		"producer":  func(value *ReportPlan) { value.Producer.Image = "latest" },
		"reference": func(value *ReportPlan) { value.Baselines[0].ID = "different" },
		"evidence": func(value *ReportPlan) {
			value.Baselines[0].Artifact.Format = "normalized-result/v2"
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := plan.Clone()
			mutate(&invalid)
			if err := invalid.Validate(fixture.current.ID); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTrustedReportPlanResolverPreservesBoundaryErrors(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	for name, boundary := range map[string]struct {
		trustErr    error
		baselineErr error
	}{
		"registry authorization": {trustErr: run.ErrForbidden},
		"registry availability":  {trustErr: run.ErrUnavailable},
		"baseline availability":  {baselineErr: run.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			planner, err := NewTrustedReportPlanResolver(
				&baselineApprovalStore{resolve: func(
					context.Context, string, baseline.Selection,
				) (baseline.Record, bool, error) {
					return fixture.record, true, boundary.baselineErr
				}},
				reportTrustResolverFunc(func(
					context.Context, string, run.Run, ReportingInput,
				) (ReportTrust, error) {
					return fixture.trust, boundary.trustErr
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			want := boundary.trustErr
			if want == nil {
				want = boundary.baselineErr
			}
			if _, err := planner.ResolveReportPlan(
				context.Background(), "principal-a", fixture.current, fixture.input,
			); !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTrustedReportPlanResolverValidatesDependenciesAndContext(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	store := &baselineApprovalStore{resolve: func(
		context.Context, string, baseline.Selection,
	) (baseline.Record, bool, error) {
		return fixture.record, true, nil
	}}
	trust := reportTrustResolverFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (ReportTrust, error) {
		return fixture.trust, nil
	})
	if _, err := NewTrustedReportPlanResolver(nil, trust); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := NewTrustedReportPlanResolver(store, nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil trust error = %v", err)
	}
	planner, err := NewTrustedReportPlanResolver(store, trust)
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := planner.ResolveReportPlan(
		canceled, "principal-a", fixture.current, fixture.input,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	if _, err := planner.ResolveReportPlan(
		context.Background(), "", fixture.current, fixture.input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid context error = %v", err)
	}
}
