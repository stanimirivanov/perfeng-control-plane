package reconcile

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// BenchmarkStore serializes target materialization with existing Run lifecycle
// and lease writes. The durable hold is independent of the worker lease.
type BenchmarkStore interface {
	ProvisioningStore
	run.BenchmarkReservationStore
}

// BenchmarkTarget admits actual observations and creates only reservation-bound
// resources. No method releases a target or performs broad cleanup.
type BenchmarkTarget interface {
	// Qualify returns accepted fresh evidence or a safe error without target writes.
	// Failed/missing observations return benchmark.ErrNotQualified; context errors
	// remain cancellable, and dependency failures must not expose provider payloads.
	Qualify(context.Context, benchmark.Spec) ([]byte, error)
	// EnsureNamespace checks cluster identity, then creates/adopts an exact owned namespace.
	EnsureNamespace(context.Context, run.BenchmarkReservation, kubernetes.BenchmarkFence) (string, error)
	// CheckNamespace verifies incarnation and ownership without creating a replacement.
	CheckNamespace(context.Context, run.BenchmarkReservation) error
	// EnsureResources rechecks the supplied storage fence and target UID before each write.
	EnsureResources(context.Context, benchmark.Spec, run.BenchmarkReservation, kubernetes.BenchmarkFence) error
}

// BenchmarkReconciler intercepts contained dispatch and cancellation while
// delegating unrelated Runs and later stages to the ordinary lifecycle router.
type BenchmarkReconciler struct {
	store      BenchmarkStore
	resolver   BenchmarkResolver
	target     BenchmarkTarget
	jobs       JobDispatcher
	controller ExecutionController
	bound      worker.Reconciler
	fallback   worker.Reconciler
	config     ProvisioningConfig
}

// NewBenchmarkReconciler refuses partial composition; admitted OCI benchmarks
// must never fall through to generic, unreserved Job dispatch.
func NewBenchmarkReconciler(
	store BenchmarkStore, resolver BenchmarkResolver, target BenchmarkTarget,
	jobs JobDispatcher, controller ExecutionController,
	bound, fallback worker.Reconciler, config ProvisioningConfig,
) (*BenchmarkReconciler, error) {
	if store == nil || resolver == nil || (target == nil && resolver.HasBenchmarks()) ||
		jobs == nil || controller == nil || bound == nil || fallback == nil || config.Validate() != nil {
		return nil, run.ErrValidation
	}

	return &BenchmarkReconciler{store: store, resolver: resolver, target: target, jobs: jobs, controller: controller, bound: bound, fallback: fallback, config: config}, nil
}

// Reconcile reserves before namespace creation and seals before authority or Job
// creation. Ambiguous creates are adopted by exact identity, never replaced.
// Cancellation only stops a known executor; target cleanup/release is a separate
// capability, so cancellation deliberately remains nonterminal with its hold.
func (reconciler *BenchmarkReconciler) Reconcile(ctx context.Context, claim run.Claim) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}

	switch claim.Run.State {
	case run.StateProvisioning, run.StateWarmingUp, run.StateRunning, run.StateCancelling:
	default:
		return reconciler.fallback.Reconcile(ctx, claim)
	}

	attempt, cancel := context.WithTimeout(ctx, reconciler.config.AttemptTimeout)
	defer cancel()

	reservation, held, err := reconciler.store.GetBenchmarkReservation(attempt, claim.Lease)
	if err != nil {
		return worker.Result{}, err
	}

	var plan BenchmarkPlan
	if held {
		plan, err = RestoreBenchmarkPlan(reservation.Intent)
	} else {
		var selected bool
		plan, selected, err = reconciler.resolver.ResolveBenchmark(attempt, claim.Lease.Principal, claim.Run.Clone())
		if err == nil && !selected {
			return reconciler.fallback.Reconcile(ctx, claim)
		}
	}
	if err != nil {
		return worker.Result{}, err
	}
	if plan.Validate() != nil {
		return worker.Result{}, run.ErrValidation
	}
	if reconciler.target == nil {
		return worker.Result{}, run.ErrUnavailable
	}

	execution, bound, err := reconciler.store.GetExecution(attempt, claim.Lease)
	if err != nil {
		return worker.Result{}, err
	}
	if bound && (!held || !execution.Valid() || execution.RunID != claim.Run.ID) {
		return worker.Result{}, run.ErrBenchmarkConflict
	}

	if claim.Run.State == run.StateCancelling {
		return reconciler.cancelBenchmark(attempt, claim, plan, reservation, held, execution, bound)
	}
	if bound {
		return reconciler.bound.Reconcile(attempt, claim)
	}
	if claim.Run.State != run.StateProvisioning {
		return worker.Result{}, ErrExecutionNotBound
	}
	if held && len(reservation.Request) != 0 {
		found, err := reconciler.recoverJob(attempt, claim, plan, reservation)
		if err != nil || found {
			return worker.Result{}, err
		}
	}

	return worker.Result{}, reconciler.dispatch(attempt, claim, plan)
}

func (reconciler *BenchmarkReconciler) dispatch(ctx context.Context, claim run.Claim, plan BenchmarkPlan) error {
	intent, err := plan.Intent()
	if err != nil {
		return err
	}
	admission, err := reconciler.target.Qualify(ctx, plan.Selection)
	if err != nil {
		return err
	}

	reservation, err := reconciler.store.ReserveBenchmark(ctx, claim.Lease, claim.Run.Revision, intent, admission)
	if err != nil {
		return err
	}

	fence := func(ctx context.Context) error { return reconciler.fence(ctx, claim, reservation) }
	uid, err := reconciler.target.EnsureNamespace(ctx, reservation, fence)
	if err != nil {
		return err
	}
	reservation, err = reconciler.store.BindBenchmarkNamespace(ctx, claim.Lease, claim.Run.Revision, intent.PlanSHA256, uid)
	if err != nil {
		return err
	}
	request, err := benchmark.RequestBytes(plan.Selection, claim.Run.ID, uid)
	if err != nil {
		return err
	}
	reservation, err = reconciler.store.SealBenchmarkRequest(ctx, claim.Lease, claim.Run.Revision, intent.PlanSHA256, request)
	if err != nil {
		return err
	}

	if err := reconciler.target.EnsureResources(ctx, plan.Selection, reservation, fence); err != nil {
		return err
	}
	template, err := kubernetes.BindBenchmarkJob(plan.Template, plan.Selection, reservation)
	if err != nil {
		return err
	}
	if _, err := reconciler.target.Qualify(ctx, plan.Selection); err != nil {
		return err
	}
	if err := fence(ctx); err != nil {
		return err
	}
	if err := reconciler.target.CheckNamespace(ctx, reservation); err != nil {
		return err
	}

	dispatch, err := reconciler.jobs.EnsureJob(ctx, claim.Run.ID, template)
	if err != nil {
		return err
	}
	execution := dispatch.Execution()
	if !execution.Valid() || execution.RunID != claim.Run.ID {
		return run.ErrValidation
	}

	return reconciler.store.BindExecution(ctx, claim.Lease, execution)
}

func (reconciler *BenchmarkReconciler) fence(ctx context.Context, claim run.Claim, expected run.BenchmarkReservation) error {
	current, err := reconciler.store.RenewClaim(ctx, claim.Lease, reconciler.config.LeaseTTL)
	if err != nil {
		return err
	}
	if current.Run.Revision != claim.Run.Revision {
		return run.ErrRevision
	}
	if current.Run.State != run.StateProvisioning {
		return run.ErrTransition
	}
	stored, found, err := reconciler.store.GetBenchmarkReservation(ctx, claim.Lease)
	if err != nil {
		return err
	}
	if !found || stored.RunID != claim.Run.ID || !stored.Intent.Same(expected.Intent) ||
		stored.NamespaceUID != expected.NamespaceUID || string(stored.Request) != string(expected.Request) {
		return run.ErrBenchmarkConflict
	}
	return nil
}

func (reconciler *BenchmarkReconciler) recoverJob(ctx context.Context, claim run.Claim, plan BenchmarkPlan, reservation run.BenchmarkReservation) (bool, error) {
	if err := reconciler.target.CheckNamespace(ctx, reservation); err != nil {
		return false, err
	}
	template, err := kubernetes.BindBenchmarkJob(plan.Template, plan.Selection, reservation)
	if err != nil {
		return false, err
	}
	dispatch, found, err := reconciler.jobs.FindJob(ctx, claim.Run.ID, template)
	if err != nil || !found {
		return false, err
	}
	execution := dispatch.Execution()
	if !execution.Valid() || execution.RunID != claim.Run.ID {
		return false, run.ErrValidation
	}
	return true, reconciler.store.BindExecution(ctx, claim.Lease, execution)
}

func (reconciler *BenchmarkReconciler) cancelBenchmark(
	ctx context.Context, claim run.Claim, plan BenchmarkPlan,
	reservation run.BenchmarkReservation, held bool,
	execution kubernetes.Execution, bound bool,
) (worker.Result, error) {
	if !bound && held && len(reservation.Request) != 0 {
		found, err := reconciler.recoverJob(ctx, claim, plan, reservation)
		if err != nil {
			return worker.Result{}, err
		}
		if found {
			var err error
			execution, bound, err = reconciler.store.GetExecution(ctx, claim.Lease)
			if err != nil {
				return worker.Result{}, err
			}
		}
	}
	if bound {
		current, err := reconciler.store.RenewClaim(ctx, claim.Lease, reconciler.config.LeaseTTL)
		if err != nil {
			return worker.Result{}, err
		}
		if current.Run.State != run.StateCancelling || current.Run.Revision != claim.Run.Revision {
			return worker.Result{}, nil
		}
		if err := reconciler.controller.RequestJobStop(ctx, execution); err != nil {
			return worker.Result{}, err
		}
	}
	return worker.Result{RetryAfter: 5 * time.Second}, nil
}
