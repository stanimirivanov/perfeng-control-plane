package reconcile

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestBenchmarkCleanupRestartsFromCutoffWithoutRedispatch(t *testing.T) {
	reconciler, store, target, jobs, resolver := newBenchmarkReconcilerFixture(t)
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	store.claim.Run.State = run.StateCancelling
	store.claim.Run.Revision++
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, event := range []string{"begin-cleanup", "close-quota", "stop", "record-fence"} {
		index := slices.Index(store.events, event)
		if index <= last {
			t.Fatalf("unsafe cleanup order %s: %v", event, store.events)
		}
		last = index
	}
	if slices.Contains(store.events, "cleanup") || slices.Contains(store.events, "finish-cleanup") {
		t.Fatal("cleanup bypassed drain")
	}
	resolver.err = run.ErrForbidden
	store.reservation.Cleanup.Ready = true
	target.clean = false
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(store.events, "finish-cleanup") {
		t.Fatal("incomplete target released")
	}
	target.clean = true
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(store.events, "finish-cleanup") || store.advanced.State != run.StateAborted || jobs.creates != 1 {
		t.Fatalf("restart did not safely finish: %v", store.events)
	}
}

func TestBenchmarkCleanupLeaseLossAndIdentityConflictRetainHold(t *testing.T) {
	for _, failure := range []error{run.ErrLeaseLost, run.ErrBenchmarkConflict, run.ErrUnavailable} {
		t.Run(failure.Error(), func(t *testing.T) {
			reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
			if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
				t.Fatal(err)
			}
			store.claim.Run.State = run.StateCancelling
			store.claim.Run.Revision++
			if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
				t.Fatal(err)
			}
			store.reservation.Cleanup.Ready = true
			if errors.Is(failure, run.ErrLeaseLost) {
				store.renewErr = failure
			} else {
				target.cleanupErr = failure
			}
			store.events = nil
			if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, failure) {
				t.Fatalf("failure=%v", err)
			}
			if !store.reservation.Cleanup.ReleasedAt.IsZero() || slices.Contains(store.events, "finish-cleanup") || jobs.creates != 1 {
				t.Fatal("failed cleanup released or recreated work")
			}
		})
	}
}

type completedBenchmarkController struct{ benchmarkExecutionController }

func (completedBenchmarkController) ObserveJob(context.Context, kubernetes.Execution) (kubernetes.Observation, error) {
	return kubernetes.Observation{Phase: kubernetes.JobSucceeded}, nil
}

type missingBenchmarkController struct{ benchmarkExecutionController }

func (missingBenchmarkController) ObserveJob(context.Context, kubernetes.Execution) (kubernetes.Observation, error) {
	return kubernetes.Observation{Phase: kubernetes.JobAbsent}, nil
}

func TestBenchmarkInfrastructureFailureWaitsForOwnedCleanup(t *testing.T) {
	reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	store.claim.Run.State = run.StateRunning
	store.claim.Run.Revision++
	reconciler.controller = &missingBenchmarkController{benchmarkExecutionController{store: store}}
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if store.reservation.Cleanup == nil || store.reservation.Cleanup.Next.State != run.StateInfrastructureFailure || store.advanced.State != "" {
		t.Fatal("infrastructure failure bypassed owned cleanup")
	}
	store.reservation.Cleanup.Ready = true
	target.clean = true
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if store.advanced.State != run.StateInfrastructureFailure || store.advanced.Failure == nil ||
		store.advanced.Failure.Code != run.FailureCodeInfrastructureError || jobs.creates != 1 {
		t.Fatal("cleanup lost the failure outcome or recreated the executor")
	}
}

func TestBenchmarkCompletionPreservesCollectionThenCleansBeforeAnalysis(t *testing.T) {
	reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	store.claim.Run.State = run.StateRunning
	store.claim.Run.Revision++
	reconciler.controller = &completedBenchmarkController{benchmarkExecutionController{store: store}}
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if store.reservation.Cleanup != nil {
		t.Fatal("raw capture discarded before collection")
	}
	store.claim.Run.State = run.StateCollecting
	store.claim.Run.Revision++
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	store.claim.Run.State = run.StateAnalyzing
	store.claim.Run.Revision++
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if store.reservation.Cleanup == nil || store.reservation.Cleanup.Next.State != run.StateAnalyzing {
		t.Fatal("analysis bypassed cleanup")
	}
	store.claim.Run.State = run.StateCancelling
	store.claim.Run.Revision++
	store.reservation.Cleanup.Ready = true
	target.clean = true
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if store.advanced.State != run.StateAborted || jobs.creates != 1 {
		t.Fatal("collection beat cancellation")
	}
}
