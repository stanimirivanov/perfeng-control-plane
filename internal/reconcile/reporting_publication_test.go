package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type publishReportFunc func(context.Context, string, run.Run, ReportingInput, run.Artifact) error

func (publish publishReportFunc) PublishReport(ctx context.Context, principal string, current run.Run, input ReportingInput, report run.Artifact) error {
	return publish(ctx, principal, current, input, report)
}

func TestPublishingReportingOrdersRegistrationPublicationAndCompletion(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		base, store, claim, executions := reportingFixture(t)
		if recovered {
			store.artifacts = append(store.artifacts, reportArtifact(claim.Run.ID))
		}
		calls := 0
		publisher := publishReportFunc(func(ctx context.Context, principal string, current run.Run, input ReportingInput, report run.Artifact) error {
			calls++
			if principal != claim.Lease.Principal || current.ID != claim.Run.ID || current.Request != claim.Run.Request ||
				input.Candidate != normalizedArtifact(claim.Run.ID) || report != reportArtifact(claim.Run.ID) {
				t.Fatal("publication received substituted scope/evidence")
			}
			if _, bounded := ctx.Deadline(); !bounded || store.advancingStore.calls != 0 || (!recovered && len(store.registered) != 1) {
				t.Fatal("publication escaped attempt deadline or registration/completion ordering")
			}
			current.Request.TestSuite = "mutated"
			input.Candidate.ID, report.ID = "mutated", "mutated"

			return nil
		})
		reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, DefaultReportingConfig())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
			t.Fatal(err)
		}
		wantExecutions := 1
		if recovered {
			wantExecutions = 0
		}
		if calls != 1 || *executions != wantExecutions || len(store.registered) != wantExecutions ||
			store.change.State != run.StateCompleted || store.lease != claim.Lease || store.revision != claim.Run.Revision ||
			claim.Run.Request.TestSuite == "mutated" {
			t.Fatal("publication changed evidence or completion fencing")
		}
	}
}

func TestPublishingReportingRetriesWithoutRegeneration(t *testing.T) {
	for _, failure := range []error{run.ErrUnavailable, run.ErrForbidden, run.ErrNotFound, run.ErrValidation, run.ErrLeaseLost, context.Canceled, context.DeadlineExceeded, ErrReportFailed} {
		t.Run(failure.Error(), func(t *testing.T) {
			base, store, claim, executions := reportingFixture(t)
			calls := 0
			publisher := publishReportFunc(func(context.Context, string, run.Run, ReportingInput, run.Artifact) error {
				calls++
				if calls == 1 {
					return failure
				}

				return nil
			})
			reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, DefaultReportingConfig())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, failure) || store.advancingStore.calls != 0 {
				t.Fatal("publication failure completed or became a process verdict", err)
			}
			if _, err := reconciler.Reconcile(context.Background(), claim); err != nil || calls != 2 || *executions != 1 ||
				len(store.registered) != 1 || store.advancingStore.calls != 1 || store.change.State != run.StateCompleted {
				t.Fatal("publication retry regenerated report or failed to complete", err)
			}
		})
	}
}

func TestPublishingReportingReplaysAfterAdvanceFailure(t *testing.T) {
	for _, failure := range []error{run.ErrUnavailable, run.ErrRevision, run.ErrLeaseLost} {
		base, store, claim, executions := reportingFixture(t)
		store.advancingStore.err = failure
		calls := 0
		publisher := publishReportFunc(func(context.Context, string, run.Run, ReportingInput, run.Artifact) error {
			calls++ // The real publisher must perform retained idempotent replay here.
			return nil
		})
		reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, DefaultReportingConfig())
		if err != nil {
			t.Fatal(err)
		}
		_, err = reconciler.Reconcile(context.Background(), claim)
		if (errors.Is(failure, run.ErrRevision) && err != nil) || (!errors.Is(failure, run.ErrRevision) && !errors.Is(err, failure)) {
			t.Fatal("completion changed existing revision/availability semantics", err)
		}
		store.advancingStore.err = nil
		if _, err := reconciler.Reconcile(context.Background(), claim); err != nil || calls != 2 || *executions != 1 ||
			len(store.registered) != 1 || store.advancingStore.calls != 2 {
			t.Fatal("advance recovery did not reuse publication/report", err)
		}
	}
}

func TestPublishingReportingBoundsPublicationAttempt(t *testing.T) {
	base, store, claim, _ := reportingFixture(t)
	store.artifacts = append(store.artifacts, reportArtifact(claim.Run.ID))
	publisher := publishReportFunc(func(ctx context.Context, _ string, _ run.Run, _ ReportingInput, _ run.Artifact) error {
		<-ctx.Done()

		return ctx.Err()
	})
	config := DefaultReportingConfig()
	config.AttemptTimeout = 5 * time.Millisecond
	reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, context.DeadlineExceeded) || store.advancingStore.calls != 0 {
		t.Fatal("publication timeout completed Run", err)
	}
}

func TestPublishingReportingDoesNotCompleteAfterPublisherCancels(t *testing.T) {
	base, store, claim, _ := reportingFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := publishReportFunc(func(context.Context, string, run.Run, ReportingInput, run.Artifact) error {
		cancel()
		return nil // A publisher may finish just as its caller loses the attempt.
	})
	reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, DefaultReportingConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(ctx, claim); !errors.Is(err, context.Canceled) || store.advancingStore.calls != 0 || len(store.registered) != 1 {
		t.Fatal("cancelled publication attempt completed Run", err)
	}
}

func TestPublishingReportingRejectsMissingDependencies(t *testing.T) {
	base, store, _, _ := reportingFixture(t)
	publisher := publishReportFunc(func(context.Context, string, run.Run, ReportingInput, run.Artifact) error { return nil })
	for _, invalid := range []struct {
		store     ReportingStore
		executor  ReportExecutor
		publisher ReportPublisher
		config    ReportingConfig
	}{
		{store, base.executor, nil, DefaultReportingConfig()},
		{nil, base.executor, publisher, DefaultReportingConfig()},
		{store, nil, publisher, DefaultReportingConfig()},
		{store, base.executor, publisher, ReportingConfig{}},
	} {
		if got, err := NewPublishingReportingReconciler(invalid.store, invalid.executor, invalid.publisher, invalid.config); !errors.Is(err, run.ErrValidation) || got != nil {
			t.Fatal("invalid opt-in dependencies accepted", err)
		}
	}
}

func TestPublishingReportingSkipsPublicationBeforeValidRegistration(t *testing.T) {
	for _, failure := range []string{"listing", "registration", "evidence", "executor", "output", "state", "claim", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			base, store, claim, _ := reportingFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "listing":
				store.listErr = run.ErrUnavailable
			case "registration":
				store.registerErr = run.ErrArtifactConflict
			case "evidence":
				store.artifacts = nil
			case "executor":
				base.executor = reportFunc(func(context.Context, string, run.Run, ReportingInput) (run.Artifact, error) {
					return run.Artifact{}, ErrReportPending
				})
			case "output":
				base.executor = reportFunc(func(context.Context, string, run.Run, ReportingInput) (run.Artifact, error) {
					return run.Artifact{}, nil
				})
			case "state":
				claim.Run.State = run.StateAnalyzing
			case "claim":
				claim.Lease.RunID = "wrong"
			case "cancelled":
				cancel()
			}
			calls := 0
			publisher := publishReportFunc(func(context.Context, string, run.Run, ReportingInput, run.Artifact) error { calls++; return nil })
			reconciler, err := NewPublishingReportingReconciler(store, base.executor, publisher, DefaultReportingConfig())
			if err != nil {
				t.Fatal(err)
			}
			result, err := reconciler.Reconcile(ctx, claim)
			if failure == "executor" {
				if err != nil || result.RetryAfter != DefaultReportingConfig().RetryAfter {
					t.Fatal("pending report changed retry semantics", err)
				}
			} else if err == nil {
				t.Fatal("invalid reporting attempt unexpectedly succeeded")
			}
			if calls != 0 || store.advancingStore.calls != 0 {
				t.Fatal("unregistered/invalid reporting invoked publication or completion")
			}
		})
	}
}
