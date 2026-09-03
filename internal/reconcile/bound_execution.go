package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// ErrExecutionNotBound means reconciliation reached an execution state before
// a durable Kubernetes identity was associated with the Run.
var ErrExecutionNotBound = errors.New("run has no durable Kubernetes execution identity")

// BoundExecutionStore is the lease-fenced storage required after dispatch.
type BoundExecutionStore interface {
	ClaimAdvancer
	GetExecution(context.Context, run.Lease) (kubernetes.Execution, bool, error)
}

// ExecutionController observes and stops an exact Kubernetes execution.
type ExecutionController interface {
	ObserveJob(context.Context, kubernetes.Execution) (kubernetes.Observation, error)
	RequestJobStop(context.Context, kubernetes.Execution) error
}

// TerminationVerifier confirms that dependent execution Pods are gone.
type TerminationVerifier interface {
	ConfirmExecutionStopped(context.Context, kubernetes.Execution) (bool, error)
}

// BoundExecutionConfig controls rediscovery and cancellation observation.
type BoundExecutionConfig struct {
	RetryAfter            time.Duration
	CancellationPollEvery time.Duration
}

// DefaultBoundExecutionConfig returns conservative polling intervals.
func DefaultBoundExecutionConfig() BoundExecutionConfig {
	return BoundExecutionConfig{
		RetryAfter:            5 * time.Second,
		CancellationPollEvery: time.Second,
	}
}

// BoundExecutionReconciler applies lifecycle decisions for a persisted Job.
// Dispatch and approved-template resolution belong to an earlier stage.
type BoundExecutionReconciler struct {
	store      BoundExecutionStore
	controller ExecutionController
	verifier   TerminationVerifier
	config     BoundExecutionConfig
}

var _ worker.Reconciler = (*BoundExecutionReconciler)(nil)
var _ ExecutionController = (*kubernetes.Dispatcher)(nil)
var _ TerminationVerifier = (*kubernetes.StopVerifier)(nil)

// NewBoundExecutionReconciler validates dependencies and polling bounds.
func NewBoundExecutionReconciler(
	store BoundExecutionStore,
	controller ExecutionController,
	verifier TerminationVerifier,
	config BoundExecutionConfig,
) (*BoundExecutionReconciler, error) {
	if store == nil || controller == nil || verifier == nil ||
		config.RetryAfter <= 0 || !run.ValidRetryDelay(config.RetryAfter) ||
		config.CancellationPollEvery <= 0 || config.CancellationPollEvery > 5*time.Minute {
		return nil, run.ErrValidation
	}

	return &BoundExecutionReconciler{
		store: store, controller: controller, verifier: verifier, config: config,
	}, nil
}

// Reconcile observes and advances one bound execution. Cancellation remains in
// the attempt so the worker renews its lease until Job absence is confirmed.
func (reconciler *BoundExecutionReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !claim.Lease.Valid() || claim.Lease.RunID != claim.Run.ID || claim.Run.State == "" {
		return worker.Result{}, run.ErrValidation
	}

	execution, found, err := reconciler.store.GetExecution(ctx, claim.Lease)
	if err != nil {
		return worker.Result{}, err
	}
	if !found {
		return worker.Result{}, ErrExecutionNotBound
	}
	if !execution.Valid() || execution.RunID != claim.Run.ID {
		return worker.Result{}, run.ErrValidation
	}

	if claim.Run.State == run.StateCancelling {
		return reconciler.cancel(ctx, claim, execution)
	}

	observation, err := reconciler.controller.ObserveJob(ctx, execution)
	if err != nil {
		return worker.Result{}, err
	}
	decision, err := DecideBoundExecution(claim.Run.State, observation)
	if err != nil {
		return worker.Result{}, err
	}

	return reconciler.apply(ctx, claim, decision)
}

func (reconciler *BoundExecutionReconciler) cancel(
	ctx context.Context,
	claim run.Claim,
	execution kubernetes.Execution,
) (worker.Result, error) {
	stopRequested := false
	for {
		action, err := reconciler.cancellationAction(ctx, claim.Run.State, execution)
		if err != nil {
			return worker.Result{}, err
		}
		complete, requested, err := reconciler.continueCancellation(
			ctx,
			execution,
			action,
			stopRequested,
		)
		if err != nil {
			return worker.Result{}, err
		}
		stopRequested = requested
		if complete {
			return reconciler.apply(ctx, claim, advance(run.Change{State: run.StateAborted}))
		}
		if err := wait(ctx, reconciler.config.CancellationPollEvery); err != nil {
			return worker.Result{}, err
		}
	}
}

func (reconciler *BoundExecutionReconciler) cancellationAction(
	ctx context.Context,
	state run.State,
	execution kubernetes.Execution,
) (Action, error) {
	observation, err := reconciler.controller.ObserveJob(ctx, execution)
	if err != nil {
		return "", err
	}
	decision, err := DecideBoundExecution(state, observation)

	return decision.Action, err
}

func (reconciler *BoundExecutionReconciler) continueCancellation(
	ctx context.Context,
	execution kubernetes.Execution,
	action Action,
	stopRequested bool,
) (bool, bool, error) {
	switch action {
	case ActionStop:
		if stopRequested {
			return false, true, nil
		}
		err := reconciler.controller.RequestJobStop(ctx, execution)

		return false, err == nil, err
	case ActionConfirmStop:
		stopped, err := reconciler.verifier.ConfirmExecutionStopped(ctx, execution)

		return stopped, stopRequested, err
	default:
		return false, stopRequested, run.ErrValidation
	}
}

func (reconciler *BoundExecutionReconciler) apply(
	ctx context.Context,
	claim run.Claim,
	decision Decision,
) (worker.Result, error) {
	if decision.Action == ActionWait {
		return worker.Result{RetryAfter: reconciler.config.RetryAfter}, nil
	}
	if decision.Action != ActionAdvance {
		return worker.Result{}, run.ErrValidation
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, decision.Change)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
