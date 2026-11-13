package app

import (
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

func TestRerunJobCompositionIsExplicitAndKeepsRegistryCapabilities(t *testing.T) {
	if value, err := composeRerunJobs(false, nil, nil); err != nil || value != nil {
		t.Fatal("disabled mode changed", err)
	}
	if _, err := composeRerunJobs(true, &startupDatabaseStub{}, &registry.ReportPolicyRegistry{}); err == nil {
		t.Fatal("missing store capability admitted")
	}
	guarded, err := composeRerunJobs(true, &postgres.Repository{}, &registry.ReportPolicyRegistry{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := guarded.(*reconcile.RerunJobResolver); !ok {
		t.Fatal("capture composition lacks guard")
	}
	dependencies, client := reconciliationDependencies(t)
	original := dependencies.Jobs
	dependencies.RerunJobs = guarded
	if _, err := NewReconciliationWorker(dependencies, func(worker.Event) {}, reconciliationWorkerConfig(), completeReconciliationConfig()); err != nil {
		t.Fatal(err)
	}
	if dependencies.Jobs != original || len(client.Actions()) != 0 {
		t.Fatal("composition changed registry discovery or contacted Kubernetes")
	}
}
