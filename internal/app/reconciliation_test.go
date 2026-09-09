package app

import (
	"context"
	"errors"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type inertReconciliationStore struct{}

func (inertReconciliationStore) ClaimRuns(
	context.Context,
	string,
	int,
	time.Duration,
) ([]run.Claim, error) {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) RenewClaim(
	context.Context,
	run.Lease,
	time.Duration,
) (run.Claim, error) {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) ReleaseClaim(
	context.Context,
	run.Lease,
	time.Duration,
) error {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) AdvanceClaim(
	context.Context,
	run.Lease,
	int64,
	run.Change,
) (run.Run, error) {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) RegisterArtifact(
	context.Context,
	string,
	run.Artifact,
) error {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) GetArtifact(
	context.Context,
	string,
	string,
	string,
) (run.Artifact, error) {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) ListArtifacts(
	context.Context,
	string,
	string,
) ([]run.Artifact, error) {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) BindExecution(
	context.Context,
	run.Lease,
	kubernetes.Execution,
) error {
	panic("reconciliation store called during construction")
}

func (inertReconciliationStore) GetExecution(
	context.Context,
	run.Lease,
) (kubernetes.Execution, bool, error) {
	panic("reconciliation store called during construction")
}

type inertJobResolver struct{}

func (inertJobResolver) ResolveJob(
	context.Context,
	string,
	run.Run,
) (*batchv1.Job, error) {
	panic("Job resolver called during construction")
}

func reconciliationWorkerConfig() worker.Config {
	return worker.Config{
		WorkerID: "worker-a", Capacity: 2, LeaseTTL: 30 * time.Second,
		RenewEvery: 10 * time.Second, PollEvery: time.Second, FailureDelay: 5 * time.Second,
	}
}

func completeReconciliationConfig() ReconciliationConfig {
	return ReconciliationConfig{
		ValidationAttemptTimeout: 10 * time.Second,
		Provisioning:             reconcile.DefaultProvisioningConfig(),
		BoundExecution:           reconcile.DefaultBoundExecutionConfig(),
		Collection:               reconcile.DefaultCollectionConfig(),
		Analysis:                 reconcile.DefaultAnalysisConfig(),
		Reporting:                reconcile.DefaultReportingConfig(),
	}
}

func reconciliationDependencies(t *testing.T) (ReconciliationDependencies, *fake.Clientset) {
	t.Helper()
	trust, baselines, _, collectors := executorDependencies(t)
	clients, kubernetesClient := namespacedKubernetesClients()
	controllers, err := NewKubernetesControllers(clients)
	if err != nil {
		t.Fatal(err)
	}
	executors, err := NewKubernetesExecutors(
		trust,
		baselines,
		controllers,
		collectors,
		kubernetesExecutorConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return ReconciliationDependencies{
		Store:       inertReconciliationStore{},
		Jobs:        inertJobResolver{},
		Controllers: controllers,
		Collectors:  collectors,
		Executors:   executors,
	}, kubernetesClient
}

func TestNewReconciliationWorkerComposesEveryStageWithoutRequests(t *testing.T) {
	dependencies, kubernetesClient := reconciliationDependencies(t)
	report := func(worker.Event) { panic("event reporter called during construction") }

	runtime, err := NewReconciliationWorker(
		dependencies,
		report,
		reconciliationWorkerConfig(),
		completeReconciliationConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		t.Fatal("worker is nil")
	}
	if actions := kubernetesClient.Actions(); len(actions) != 0 {
		t.Fatalf("construction made Kubernetes requests: %#v", actions)
	}
}

func TestNewReconciliationWorkerRejectsIncompleteComposition(t *testing.T) {
	valid, _ := reconciliationDependencies(t)
	validWorker := reconciliationWorkerConfig()
	validReconciliation := completeReconciliationConfig()
	report := func(worker.Event) {}
	tests := map[string]func() (*worker.Worker, error){
		"store": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Store = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"Job resolver": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Jobs = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"execution controller": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Controllers.Execution = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"termination controller": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Controllers.Termination = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"raw collector": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Collectors.Raw = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"analysis executor": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Executors.Analysis = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"reporting executor": func() (*worker.Worker, error) {
			invalid := valid
			invalid.Executors.Reporting = nil

			return NewReconciliationWorker(invalid, report, validWorker, validReconciliation)
		},
		"event reporter": func() (*worker.Worker, error) {
			return NewReconciliationWorker(valid, nil, validWorker, validReconciliation)
		},
		"worker config": func() (*worker.Worker, error) {
			invalid := validWorker
			invalid.Capacity = 0

			return NewReconciliationWorker(valid, report, invalid, validReconciliation)
		},
		"reconciliation config": func() (*worker.Worker, error) {
			invalid := validReconciliation
			invalid.Provisioning.LeaseTTL = time.Minute

			return NewReconciliationWorker(valid, report, validWorker, invalid)
		},
	}

	for name, construct := range tests {
		t.Run(name, func(t *testing.T) {
			runtime, err := construct()
			if runtime != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("worker = %#v, error = %v", runtime, err)
			}
		})
	}
}

var _ ReconciliationStore = inertReconciliationStore{}
var _ reconcile.JobResolver = inertJobResolver{}
