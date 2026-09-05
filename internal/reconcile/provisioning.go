package reconcile

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// JobResolver resolves and authorizes immutable resources for the principal.
// It must return an independently owned template reproducible from the Run ID
// and pinned request, not mutable lifecycle state. It must honor cancellation
// and return safe errors without credentials or raw registry responses.
type JobResolver interface {
	// ResolveJob returns an independently owned, approved template for principal
	// and the immutable accepted Run request.
	ResolveJob(context.Context, string, run.Run) (*batchv1.Job, error)
}

// ProvisioningStore preserves the execution-store and renewal contracts while
// keeping Kubernetes I/O outside storage transactions.
type ProvisioningStore interface {
	kubernetes.ExecutionStore
	// RenewClaim rechecks ownership and returns the latest Run before dispatch.
	RenewClaim(context.Context, run.Lease, time.Duration) (run.Claim, error)
}

// JobDispatcher creates or adopts a matching deterministic Job without replacing
// conflicting executions. A successful result must include a durable identity.
type JobDispatcher interface {
	// EnsureJob creates or adopts the deterministic Job without replacing conflicts.
	EnsureJob(context.Context, string, *batchv1.Job) (kubernetes.Dispatch, error)
}

// ProvisioningConfig bounds resolution, ownership recheck, dispatch and binding
// together. LeaseTTL must match the composing worker's lease configuration.
type ProvisioningConfig struct {
	LeaseTTL       time.Duration
	AttemptTimeout time.Duration
}

// DefaultProvisioningConfig matches the default worker lease and reserves
// renewal headroom beyond the attempt deadline.
func DefaultProvisioningConfig() ProvisioningConfig {
	return ProvisioningConfig{LeaseTTL: 30 * time.Second, AttemptTimeout: 10 * time.Second}
}

// ProvisioningReconciler handles only PROVISIONING. Existing bindings are routed
// to the bound stage; absent bindings are resolved, dispatched and persisted.
type ProvisioningReconciler struct {
	store      ProvisioningStore
	resolver   JobResolver
	dispatcher JobDispatcher
	bound      worker.Reconciler
	config     ProvisioningConfig
}

var _ worker.Reconciler = (*ProvisioningReconciler)(nil)
var _ JobDispatcher = (*kubernetes.Dispatcher)(nil)

// NewProvisioningReconciler requires a bound stage so persisted executions
// cannot re-enter creation, even if their Kubernetes Job has disappeared.
func NewProvisioningReconciler(
	store ProvisioningStore,
	resolver JobResolver,
	dispatcher JobDispatcher,
	bound worker.Reconciler,
	config ProvisioningConfig,
) (*ProvisioningReconciler, error) {
	if store == nil || resolver == nil || dispatcher == nil || bound == nil ||
		!run.ValidLeaseTTL(config.LeaseTTL) ||
		config.AttemptTimeout <= 0 || config.AttemptTimeout > config.LeaseTTL/2 {
		return nil, run.ErrValidation
	}

	return &ProvisioningReconciler{
		store: store, resolver: resolver, dispatcher: dispatcher, bound: bound, config: config,
	}, nil
}

// Reconcile leaves lifecycle state unchanged after binding a new execution;
// the next attempt routes it to the bound stage. An error may follow a committed
// create or binding, so retries must always start by checking durable identity.
func (reconciler *ProvisioningReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !claim.Lease.Valid() || claim.Lease.RunID != claim.Run.ID || claim.Run.Revision < 1 {
		return worker.Result{}, run.ErrValidation
	}
	if claim.Run.State != run.StateProvisioning {
		return worker.Result{}, ErrStateNotHandled
	}

	ctx, cancel := context.WithTimeout(ctx, reconciler.config.AttemptTimeout)
	defer cancel()

	execution, found, err := reconciler.store.GetExecution(ctx, claim.Lease)
	if err != nil {
		return worker.Result{}, err
	}
	if found {
		if !execution.Valid() || execution.RunID != claim.Run.ID {
			return worker.Result{}, run.ErrValidation
		}

		return reconciler.bound.Reconcile(ctx, claim)
	}

	return worker.Result{}, reconciler.dispatchAndBind(ctx, claim)
}

func (reconciler *ProvisioningReconciler) dispatchAndBind(ctx context.Context, claim run.Claim) error {
	template, err := reconciler.resolver.ResolveJob(ctx, claim.Lease.Principal, claim.Run.Clone())
	if err != nil {
		return err
	}
	if template == nil {
		return run.ErrValidation
	}

	current, err := reconciler.store.RenewClaim(ctx, claim.Lease, reconciler.config.LeaseTTL)
	if err != nil {
		return err
	}
	if current.Run.State != run.StateProvisioning || current.Run.Revision != claim.Run.Revision {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dispatch, err := reconciler.dispatcher.EnsureJob(ctx, claim.Run.ID, template)
	if err != nil {
		return err
	}
	execution := dispatch.Execution()
	if !execution.Valid() || execution.RunID != claim.Run.ID {
		return run.ErrValidation
	}

	return reconciler.store.BindExecution(ctx, claim.Lease, execution)
}
