package reconcile

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// Router sends each active lifecycle state to exactly one reconciliation stage.
// Analysis and reporting wait for their later stage implementations.
type Router struct {
	store         ClaimAdvancer
	validation    worker.Reconciler
	provisioning  worker.Reconciler
	bound         worker.Reconciler
	collection    worker.Reconciler
	deferredRetry time.Duration
}

var _ worker.Reconciler = (*Router)(nil)

// NewRouter validates every implemented stage and the deferred retry interval.
func NewRouter(
	store ClaimAdvancer,
	validation worker.Reconciler,
	provisioning worker.Reconciler,
	bound worker.Reconciler,
	collection worker.Reconciler,
	deferredRetry time.Duration,
) (*Router, error) {
	if store == nil || validation == nil || provisioning == nil || bound == nil || collection == nil ||
		deferredRetry <= 0 || !run.ValidRetryDelay(deferredRetry) {
		return nil, run.ErrValidation
	}

	return &Router{
		store: store, validation: validation, provisioning: provisioning,
		bound: bound, collection: collection, deferredRetry: deferredRetry,
	}, nil
}

// Reconcile advances CREATED once, routes implemented stages, and quietly
// defers states whose analysis or reporting components do not exist yet.
func (router *Router) Reconcile(ctx context.Context, claim run.Claim) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}

	switch claim.Run.State {
	case run.StateCreated:
		return advanceOwnedClaim(ctx, router.store, claim, run.Change{State: run.StateValidating})
	case run.StateValidating:
		return router.validation.Reconcile(ctx, claim)
	case run.StateProvisioning:
		return router.provisioning.Reconcile(ctx, claim)
	case run.StateWarmingUp, run.StateRunning, run.StateCancelling:
		return router.bound.Reconcile(ctx, claim)
	case run.StateCollecting:
		return router.collection.Reconcile(ctx, claim)
	case run.StateAnalyzing, run.StateReporting:
		return worker.Result{RetryAfter: router.deferredRetry}, nil
	default:
		return worker.Result{}, ErrStateNotHandled
	}
}
