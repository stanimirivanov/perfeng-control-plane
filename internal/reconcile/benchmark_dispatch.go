package reconcile

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
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

// BenchmarkTarget admits actual observations and manages only reservation-bound
// resources. Scoped cleanup attests absence; storage alone releases the target.
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
	// InspectCleanupNamespace reads exact ownership and UID, reporting absence without creation.
	InspectCleanupNamespace(context.Context, run.BenchmarkReservation) (string, bool, error)
	// CloseBenchmarkTarget irreversibly closes Pod quota under the persisted cleanup fence.
	CloseBenchmarkTarget(context.Context, run.BenchmarkReservation, kubernetes.BenchmarkFence) error
	// CleanupBenchmark removes only owned resources and verifies executor/workload absence.
	// A false result retains the hold; foreign resources, replacement UIDs and unknown nodes fail closed.
	CleanupBenchmark(context.Context, benchmark.Spec, run.BenchmarkReservation, *kubernetes.Execution, kubernetes.BenchmarkFence) (bool, []byte, error)
}

// BenchmarkReconciler coordinates contained dispatch, owned cleanup and restart
// recovery while delegating collection and released Runs to the ordinary router.
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
// Cancellation and failure seal dispatch before cleanup; normal completion
// retains raw collection before cleanup gates analysis. Unverified termination
// keeps the durable hold, including for already-terminal recovery.
func (reconciler *BenchmarkReconciler) Reconcile(ctx context.Context, claim run.Claim) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}

	attempt, cancel := context.WithTimeout(ctx, reconciler.config.AttemptTimeout)
	defer cancel()

	reservation, held, err := reconciler.store.GetBenchmarkReservation(attempt, claim.Lease)
	if err != nil {
		return worker.Result{}, err
	}
	if held && reservation.Cleanup != nil && !reservation.Cleanup.ReleasedAt.IsZero() {
		return reconciler.fallback.Reconcile(ctx, claim)
	}
	if !held && claim.Run.State != run.StateProvisioning && claim.Run.State != run.StateCancelling {
		return reconciler.fallback.Reconcile(ctx, claim)
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

	if reservation.Cleanup != nil {
		return reconciler.cleanup(attempt, claim, plan, reservation, execution, bound)
	}
	if held && claim.Run.State == run.StateCollecting {
		return reconciler.fallback.Reconcile(attempt, claim)
	}
	if claim.Run.State == run.StateCancelling || contract.Terminal(string(claim.Run.State)) ||
		(held && claim.Run.State != run.StateProvisioning && claim.Run.State != run.StateWarmingUp && claim.Run.State != run.StateRunning) {
		if !held {
			return advanceOwnedClaim(attempt, reconciler.store, claim, run.Change{State: run.StateAborted})
		}
		next := run.Change{State: claim.Run.State}
		if claim.Run.State == run.StateCancelling {
			next.State = run.StateAborted
		}
		return reconciler.beginCleanup(attempt, claim, plan, reservation, next, execution, bound)
	}
	if bound {
		observation, err := reconciler.controller.ObserveJob(attempt, execution)
		if err != nil {
			return worker.Result{}, err
		}
		decision, err := DecideBoundExecution(claim.Run.State, observation)
		if err != nil {
			return worker.Result{}, err
		}
		if decision.Action == ActionAdvance && contract.Terminal(string(decision.Change.State)) {
			return reconciler.beginCleanup(attempt, claim, plan, reservation, decision.Change, execution, true)
		}
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
	if !found || stored.Cleanup != nil || stored.RunID != claim.Run.ID || !stored.Intent.Same(expected.Intent) ||
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
