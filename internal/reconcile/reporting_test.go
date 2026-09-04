package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type reportFunc func(context.Context, string, run.Run, ReportingInput) (run.Artifact, error)

func (report reportFunc) Report(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (run.Artifact, error) {
	return report(ctx, principal, current, input)
}

func reportArtifact(runID string) run.Artifact {
	return run.Artifact{
		ID: "10000000-0000-4000-8000-000000000005", RunID: runID, Kind: "normalized",
		URI:    "s3://perfeng-artifacts/runs/example/analysis-result.json",
		SHA256: strings.Repeat("e", 64), SizeBytes: 212,
		MediaType: "application/json", Format: "analysis-result/v1",
	}
}

func reportingArtifacts(runID string) []run.Artifact {
	return append(analysisArtifacts(runID), normalizedArtifact(runID))
}

func reportingFixture(t *testing.T) (*ReportingReconciler, *analysisStore, run.Claim, *int) {
	t.Helper()

	claim := boundClaim(run.StateReporting)
	store := &analysisStore{
		advancingStore: &advancingStore{},
		artifacts:      reportingArtifacts(claim.Run.ID),
	}
	calls := 0
	executor := reportFunc(func(
		ctx context.Context,
		principal string,
		current run.Run,
		input ReportingInput,
	) (run.Artifact, error) {
		calls++
		if principal != claim.Lease.Principal || current.ID != claim.Run.ID ||
			current.Request != claim.Run.Request {
			t.Fatal("executor did not receive the owned immutable Run")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("reporting has no attempt deadline")
		}
		if input.Candidate != normalizedArtifact(claim.Run.ID) {
			t.Fatalf("reporting input = %+v", input)
		}

		return reportArtifact(current.ID), nil
	})
	reconciler, err := NewReportingReconciler(store, executor, DefaultReportingConfig())
	if err != nil {
		t.Fatal(err)
	}

	return reconciler, store, claim, &calls
}

func TestReportingRegistersReportThenCompletes(t *testing.T) {
	reconciler, store, claim, calls := reportingFixture(t)
	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	want := reportArtifact(claim.Run.ID)
	if *calls != 1 || store.listCalls != 1 ||
		!reflect.DeepEqual(store.registered, []run.Artifact{want}) ||
		store.advancingStore.calls != 1 || store.change.State != run.StateCompleted ||
		store.change.Failure != nil || store.lease != claim.Lease ||
		store.revision != claim.Run.Revision {
		t.Fatalf("unexpected reporting effects: calls=%d, store=%+v", *calls, store)
	}
}

func TestReportingRecoversRegisteredReportWithoutDuplicateExecution(t *testing.T) {
	reconciler, store, claim, calls := reportingFixture(t)
	store.artifacts = append(store.artifacts, reportArtifact(claim.Run.ID))

	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 1 ||
		store.change.State != run.StateCompleted {
		t.Fatalf("recovery effects: calls=%d, store=%+v", *calls, store)
	}
}

func TestReportingWaitsQuietlyForPendingReport(t *testing.T) {
	reconciler, store, claim, _ := reportingFixture(t)
	reconciler.executor = reportFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrReportPending, errors.New("job still running"))
	})

	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != DefaultReportingConfig().RetryAfter ||
		len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatalf("pending = %+v, %v; store=%+v", result, err, store)
	}
}

func TestReportingPersistsSafeProcessFailure(t *testing.T) {
	reconciler, store, claim, _ := reportingFixture(t)
	reconciler.executor = reportFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrReportFailed, errors.New("secret process output"))
	})

	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	failure := store.change.Failure
	if len(store.registered) != 0 || store.advancingStore.calls != 1 ||
		store.change.State != run.StateInfrastructureFailure || failure == nil ||
		failure.Code != run.FailureCodeAnalysisError ||
		failure.Message != "analysis report generation failed" ||
		strings.Contains(failure.Message, "secret") {
		t.Fatalf("unsafe reporting failure: %+v", store.change)
	}
}

func TestReportingPreservesOperationalAndAmbiguousErrors(t *testing.T) {
	for _, want := range []error{run.ErrUnavailable, context.Canceled, run.ErrArtifactConflict} {
		t.Run(want.Error(), func(t *testing.T) {
			reconciler, store, claim, _ := reportingFixture(t)
			reconciler.executor = reportFunc(func(
				context.Context, string, run.Run, ReportingInput,
			) (run.Artifact, error) {
				return run.Artifact{}, want
			})
			if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("operational error changed durable state")
			}
		})
	}

	reconciler, store, claim, _ := reportingFixture(t)
	reconciler.executor = reportFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrReportPending, ErrReportFailed)
	})
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("ambiguous error = %v", err)
	}
	if len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("ambiguous classification changed durable state")
	}
}

func TestReportingPreservesStorageErrors(t *testing.T) {
	reconciler, store, claim, calls := reportingFixture(t)
	store.listErr = run.ErrUnavailable
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("list error = %v", err)
	}
	if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("listing error changed durable state")
	}

	reconciler, store, claim, calls = reportingFixture(t)
	store.registerErr = run.ErrArtifactConflict
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrArtifactConflict) {
		t.Fatalf("registration error = %v", err)
	}
	if *calls != 1 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("registration error advanced lifecycle")
	}
}

func TestReportingRejectsInvalidPersistedEvidence(t *testing.T) {
	claim := boundClaim(run.StateReporting)
	valid := reportingArtifacts(claim.Run.ID)
	secondCandidate := normalizedArtifact(claim.Run.ID)
	secondCandidate.ID = "20000000-0000-4000-8000-000000000004"
	secondCandidate.URI = "s3://perfeng-artifacts/runs/example/other-normalized-result.json"
	secondReport := reportArtifact(claim.Run.ID)
	secondReport.ID = "20000000-0000-4000-8000-000000000005"
	secondReport.URI = "s3://perfeng-artifacts/runs/example/other-analysis-result.json"
	tests := map[string][]run.Artifact{
		"empty":             nil,
		"missing candidate": analysisArtifacts(claim.Run.ID),
		"duplicate candidate": append(
			append([]run.Artifact{}, valid...), secondCandidate,
		),
		"duplicate report": append(
			append(append([]run.Artifact{}, valid...), reportArtifact(claim.Run.ID)), secondReport,
		),
		"unsupported normalized": append(append([]run.Artifact{}, valid...), run.Artifact{
			ID: "30000000-0000-4000-8000-000000000006", RunID: claim.Run.ID,
			Kind: "normalized", URI: "s3://perfeng-artifacts/runs/example/other.json",
			SHA256: strings.Repeat("f", 64), SizeBytes: 1,
			MediaType: "application/json", Format: "other-result/v1",
		}),
		"wrong run": append([]run.Artifact{}, valid...),
	}
	tests["wrong run"][0].RunID = "perf-20260903-120000-deadbeef"

	for name, artifacts := range tests {
		t.Run(name, func(t *testing.T) {
			reconciler, store, current, calls := reportingFixture(t)
			store.artifacts = artifacts
			if _, err := reconciler.Reconcile(context.Background(), current); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
			if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("invalid evidence changed durable state")
			}
		})
	}
}

func TestReportingRejectsInvalidExecutorOutput(t *testing.T) {
	claim := boundClaim(run.StateReporting)
	valid := reportArtifact(claim.Run.ID)
	tests := map[string]run.Artifact{
		"empty": {}, "wrong run": valid, "raw": valid, "wrong media": valid,
		"wrong format": valid, "candidate ID": valid, "source URI": valid, "invalid digest": valid,
	}
	wrongRun := valid
	wrongRun.RunID = "perf-20260903-120000-deadbeef"
	tests["wrong run"] = wrongRun
	raw := valid
	raw.Kind = "raw"
	tests["raw"] = raw
	wrongMedia := valid
	wrongMedia.MediaType = "text/html"
	tests["wrong media"] = wrongMedia
	wrongFormat := valid
	wrongFormat.Format = "normalized-result/v1"
	tests["wrong format"] = wrongFormat
	candidateID := valid
	candidateID.ID = normalizedArtifact(claim.Run.ID).ID
	tests["candidate ID"] = candidateID
	sourceURI := valid
	sourceURI.URI = rawArtifactSet(claim.Run.ID).Artifacts[0].URI
	tests["source URI"] = sourceURI
	invalidDigest := valid
	invalidDigest.SHA256 = "bad"
	tests["invalid digest"] = invalidDigest

	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			reconciler, store, current, _ := reportingFixture(t)
			reconciler.executor = reportFunc(func(
				context.Context, string, run.Run, ReportingInput,
			) (run.Artifact, error) {
				return output, nil
			})
			if _, err := reconciler.Reconcile(context.Background(), current); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
			if len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("invalid output changed durable state")
			}
		})
	}
}

func TestReportingRecoversAfterUncertainAdvance(t *testing.T) {
	reconciler, store, claim, calls := reportingFixture(t)
	store.advancingStore.err = run.ErrUnavailable
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	if *calls != 1 || len(store.registered) != 1 || store.advancingStore.calls != 1 {
		t.Fatalf("first attempt: calls=%d, store=%+v", *calls, store)
	}

	store.advancingStore.err = nil
	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || len(store.registered) != 1 || store.advancingStore.calls != 2 ||
		store.change.State != run.StateCompleted {
		t.Fatalf("recovery: calls=%d, store=%+v", *calls, store)
	}
}

func TestReportingHonorsDeadlineAndValidatesInputs(t *testing.T) {
	reconciler, store, claim, _ := reportingFixture(t)
	reconciler.config.AttemptTimeout = 5 * time.Millisecond
	reconciler.executor = reportFunc(func(
		ctx context.Context, _ string, _ run.Run, _ ReportingInput,
	) (run.Artifact, error) {
		<-ctx.Done()

		return run.Artifact{}, ctx.Err()
	})
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("timed-out reporting changed durable state")
	}

	executor := reconciler.executor
	for _, config := range []ReportingConfig{
		{},
		{AttemptTimeout: -time.Second, RetryAfter: time.Second},
		{AttemptTimeout: 5*time.Minute + time.Nanosecond, RetryAfter: time.Second},
		{AttemptTimeout: time.Second, RetryAfter: time.Millisecond},
		{AttemptTimeout: time.Second, RetryAfter: 5*time.Minute + time.Second},
	} {
		if _, err := NewReportingReconciler(store, executor, config); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("config %+v error = %v", config, err)
		}
	}
	if _, err := NewReportingReconciler(nil, executor, DefaultReportingConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewReportingReconciler(store, nil, DefaultReportingConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}

	reconciler, store, claim, _ = reportingFixture(t)
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	claim = boundClaim(run.StateAnalyzing)
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, ErrStateNotHandled) {
		t.Fatal(err)
	}
	if store.listCalls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("invalid input accessed mutable dependencies")
	}
}
