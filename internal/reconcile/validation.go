package reconcile

import (
	"context"
	"errors"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// ClaimAdvancer applies a lifecycle change under current lease and revision
// ownership. PostgreSQL's reconciliation store satisfies this boundary.
type ClaimAdvancer interface {
	AdvanceClaim(context.Context, run.Lease, int64, run.Change) (run.Run, error)
}

// JobValidator applies dispatch policy without creating a Kubernetes resource.
type JobValidator interface {
	ValidateJob(string, *batchv1.Job) error
}

// ValidationReconciler resolves and validates a trusted execution plan before
// allowing a Run to enter PROVISIONING.
type ValidationReconciler struct {
	store          ClaimAdvancer
	resolver       JobResolver
	validator      JobValidator
	attemptTimeout time.Duration
}

var _ worker.Reconciler = (*ValidationReconciler)(nil)
var _ JobValidator = (*kubernetes.Dispatcher)(nil)

// NewValidationReconciler requires a positive bounded resolver deadline.
func NewValidationReconciler(
	store ClaimAdvancer,
	resolver JobResolver,
	validator JobValidator,
	attemptTimeout time.Duration,
) (*ValidationReconciler, error) {
	if store == nil || resolver == nil || validator == nil ||
		attemptTimeout <= 0 || attemptTimeout > 5*time.Minute {
		return nil, run.ErrValidation
	}

	return &ValidationReconciler{
		store: store, resolver: resolver, validator: validator, attemptTimeout: attemptTimeout,
	}, nil
}

// Reconcile resolves the immutable request for preflight validation; success
// stores no mutable plan and advances only the Run lifecycle.
func (reconciler *ValidationReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}
	if claim.Run.State != run.StateValidating {
		return worker.Result{}, ErrStateNotHandled
	}

	ctx, cancel := context.WithTimeout(ctx, reconciler.attemptTimeout)
	defer cancel()

	template, err := reconciler.resolver.ResolveJob(ctx, claim.Lease.Principal, claim.Run.Clone())
	if err != nil {
		return reconciler.resolutionError(ctx, claim, err)
	}
	if template == nil {
		return reconciler.reject(ctx, claim, "approved execution plan is invalid")
	}
	if err := reconciler.validator.ValidateJob(claim.Run.ID, template); err != nil {
		return reconciler.resolutionError(ctx, claim, err)
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{State: run.StateProvisioning})
}

func (reconciler *ValidationReconciler) resolutionError(
	ctx context.Context,
	claim run.Claim,
	err error,
) (worker.Result, error) {
	switch {
	case errors.Is(err, run.ErrValidation):
		return reconciler.reject(ctx, claim, "approved execution plan is invalid")
	case errors.Is(err, run.ErrForbidden):
		return reconciler.reject(ctx, claim, "execution resources are not authorized")
	default:
		return worker.Result{}, err
	}
}

func (reconciler *ValidationReconciler) reject(
	ctx context.Context,
	claim run.Claim,
	message string,
) (worker.Result, error) {
	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{
		State: run.StateInvalid,
		Failure: &run.Failure{
			Code:    run.FailureCodeValidationFailed,
			Message: message,
		},
	})
}

func advanceOwnedClaim(
	ctx context.Context,
	store ClaimAdvancer,
	claim run.Claim,
	change run.Change,
) (worker.Result, error) {
	_, err := store.AdvanceClaim(ctx, claim.Lease, claim.Run.Revision, change)
	if errors.Is(err, run.ErrRevision) {
		return worker.Result{}, nil
	}

	return worker.Result{}, err
}

func validOwnedClaim(claim run.Claim) bool {
	return claim.Lease.Valid() && claim.Lease.RunID == claim.Run.ID && claim.Run.Revision >= 1
}
