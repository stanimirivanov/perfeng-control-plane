package app

import (
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// ReconciliationStore combines the durable worker, execution and artifact
// operations used by the complete lifecycle. Database satisfies this boundary.
type ReconciliationStore interface {
	run.ReconciliationStore
	run.ArtifactRepository
	kubernetes.ExecutionStore
}

// ReconciliationDependencies contains the already constructed persistence,
// trust, Kubernetes and artifact boundaries used by lifecycle reconciliation.
type ReconciliationDependencies struct {
	Store       ReconciliationStore
	Jobs        reconcile.JobResolver
	Controllers KubernetesControllers
	Collectors  ArtifactCollectors
	Executors   KubernetesExecutors
}

// NewReconciliationWorker composes every active lifecycle stage and its router
// into one bounded worker without calling persistence, Kubernetes or storage.
func NewReconciliationWorker(
	dependencies ReconciliationDependencies,
	report func(worker.Event),
	workerConfig worker.Config,
	reconciliationConfig ReconciliationConfig,
) (*worker.Worker, error) {
	if dependencies.Store == nil || dependencies.Jobs == nil ||
		dependencies.Controllers.Execution == nil ||
		dependencies.Controllers.Termination == nil ||
		dependencies.Collectors.Raw == nil ||
		dependencies.Executors.Analysis == nil ||
		dependencies.Executors.Reporting == nil ||
		report == nil || reconciliationConfig.Validate(workerConfig) != nil {
		return nil, ErrInvalidConfiguration
	}

	bound, err := reconcile.NewBoundExecutionReconciler(
		dependencies.Store,
		dependencies.Controllers.Execution,
		dependencies.Controllers.Termination,
		reconciliationConfig.BoundExecution,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	validation, err := reconcile.NewValidationReconciler(
		dependencies.Store,
		dependencies.Jobs,
		dependencies.Controllers.Execution,
		reconciliationConfig.ValidationAttemptTimeout,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	provisioning, err := reconcile.NewProvisioningReconciler(
		dependencies.Store,
		dependencies.Jobs,
		dependencies.Controllers.Execution,
		bound,
		reconciliationConfig.Provisioning,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	collection, err := reconcile.NewCollectionReconciler(
		dependencies.Store,
		dependencies.Collectors.Raw,
		reconciliationConfig.Collection,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	analysis, err := reconcile.NewAnalysisReconciler(
		dependencies.Store,
		dependencies.Executors.Analysis,
		reconciliationConfig.Analysis,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	reporting, err := reconcile.NewReportingReconciler(
		dependencies.Store,
		dependencies.Executors.Reporting,
		reconciliationConfig.Reporting,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	router, err := reconcile.NewRouter(
		dependencies.Store,
		validation,
		provisioning,
		bound,
		collection,
		analysis,
		reporting,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	runtime, err := worker.New(dependencies.Store, router, report, workerConfig)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return runtime, nil
}
