package reconcile

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// Router sends each active lifecycle state to exactly one reconciliation stage.
type Router struct {
	store        ClaimAdvancer
	validation   worker.Reconciler
	provisioning worker.Reconciler
	bound        worker.Reconciler
	collection   worker.Reconciler
	analysis     worker.Reconciler
	reporting    worker.Reconciler
}

var _ worker.Reconciler = (*Router)(nil)

// NewRouter validates the store and every active lifecycle stage.
func NewRouter(
	store ClaimAdvancer,
	validation worker.Reconciler,
	provisioning worker.Reconciler,
	bound worker.Reconciler,
	collection worker.Reconciler,
	analysis worker.Reconciler,
	reporting worker.Reconciler,
) (*Router, error) {
	if store == nil || validation == nil || provisioning == nil || bound == nil ||
		collection == nil || analysis == nil || reporting == nil {
		return nil, run.ErrValidation
	}

	return &Router{
		store: store, validation: validation, provisioning: provisioning,
		bound: bound, collection: collection, analysis: analysis, reporting: reporting,
	}, nil
}

// Reconcile advances CREATED once and routes every active lifecycle stage.
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
	case run.StateAnalyzing:
		return router.analysis.Reconcile(ctx, claim)
	case run.StateReporting:
		return router.reporting.Reconcile(ctx, claim)
	default:
		return worker.Result{}, ErrStateNotHandled
	}
}
