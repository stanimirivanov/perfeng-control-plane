package reconcile

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

func (reconciler *BenchmarkReconciler) beginCleanup(ctx context.Context, claim run.Claim, plan BenchmarkPlan, reservation run.BenchmarkReservation, next run.Change, execution kubernetes.Execution, bound bool) (worker.Result, error) {
	stored, err := reconciler.store.BeginBenchmarkCleanup(ctx, claim.Lease, claim.Run.Revision, reservation.Intent.PlanSHA256, next)
	if err != nil {
		return worker.Result{}, err
	}
	return reconciler.cleanup(ctx, claim, plan, stored, execution, bound)
}

func (reconciler *BenchmarkReconciler) cleanup(ctx context.Context, claim run.Claim, plan BenchmarkPlan, reservation run.BenchmarkReservation, execution kubernetes.Execution, bound bool) (worker.Result, error) {
	retry := worker.Result{RetryAfter: 5 * time.Second}
	fence := func(ctx context.Context) error {
		current, err := reconciler.store.RenewClaim(ctx, claim.Lease, reconciler.config.LeaseTTL)
		if err != nil {
			return err
		}
		if current.Run.Revision != claim.Run.Revision {
			return run.ErrRevision
		}
		stored, found, err := reconciler.store.GetBenchmarkReservation(ctx, claim.Lease)
		if err != nil {
			return err
		}
		if !found || !stored.Intent.Same(reservation.Intent) || stored.NamespaceUID != reservation.NamespaceUID ||
			stored.Cleanup == nil || !stored.Cleanup.ReleasedAt.IsZero() {
			return run.ErrBenchmarkConflict
		}
		return nil
	}
	uid, exists, err := reconciler.target.InspectCleanupNamespace(ctx, reservation)
	if err != nil {
		return worker.Result{}, err
	}
	if exists && reservation.NamespaceUID == "" {
		reservation, err = reconciler.store.BindBenchmarkCleanupNamespace(ctx, claim.Lease, claim.Run.Revision, reservation.Intent.PlanSHA256, uid)
		if err != nil {
			return worker.Result{}, err
		}
	}
	if exists {
		if err := reconciler.target.CloseBenchmarkTarget(ctx, reservation, fence); err != nil {
			return worker.Result{}, err
		}
	}
	if !bound && len(reservation.Request) != 0 {
		found, err := reconciler.recoverJob(ctx, claim, plan, reservation)
		if err != nil {
			return worker.Result{}, err
		}
		if found {
			execution, bound, err = reconciler.store.GetExecution(ctx, claim.Lease)
			if err != nil {
				return worker.Result{}, err
			}
		}
	}
	if err := fence(ctx); err != nil {
		return worker.Result{}, err
	}
	if bound {
		if err := reconciler.controller.RequestJobStop(ctx, execution); err != nil {
			return worker.Result{}, err
		}
	}
	reservation, err = reconciler.store.RecordBenchmarkCleanupFence(ctx, claim.Lease, claim.Run.Revision, reservation.Intent.PlanSHA256)
	if err != nil {
		return worker.Result{}, err
	}
	if !reservation.Cleanup.Ready {
		return retry, nil
	}
	var identity *kubernetes.Execution
	if bound {
		identity = &execution
	}
	complete, receipt, err := reconciler.target.CleanupBenchmark(ctx, plan.Selection, reservation, identity, fence)
	if err != nil {
		return worker.Result{}, err
	}
	if !complete {
		return retry, nil
	}
	if err := fence(ctx); err != nil {
		return worker.Result{}, err
	}
	_, err = reconciler.store.FinishBenchmarkCleanup(ctx, claim.Lease, claim.Run.Revision, reservation.Intent.PlanSHA256, receipt)
	return worker.Result{}, err
}
