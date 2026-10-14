package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type benchmarkJobResolver struct{ inertJobResolver }

func (benchmarkJobResolver) HasBenchmarks() bool { return true }
func (benchmarkJobResolver) ResolveBenchmark(context.Context, string, run.Run) (reconcile.BenchmarkPlan, bool, error) {
	panic("benchmark resolved during construction")
}

type inertBenchmarkStore struct {
	inertReconciliationStore
	run.BenchmarkReservationStore
}

func TestBenchmarkWorkerRequiresReservationAndTargetCapabilities(t *testing.T) {
	dependencies, client := reconciliationDependencies(t)
	dependencies.Jobs = benchmarkJobResolver{}
	report := func(worker.Event) {}
	construct := func() error {
		_, err := NewReconciliationWorker(dependencies, report, reconciliationWorkerConfig(), completeReconciliationConfig())
		return err
	}
	if err := construct(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("missing reservation store accepted: %v", err)
	}
	dependencies.Store = inertBenchmarkStore{}
	if err := construct(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("missing target adapter accepted: %v", err)
	}
	target, err := kubernetes.NewBenchmarkController(client, func(context.Context) ([]byte, error) { panic("metrics read during construction") }, "perf-generators", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	dependencies.Controllers.Benchmark = target
	if err := construct(); err != nil {
		t.Fatal(err)
	}
	if len(client.Actions()) != 0 {
		t.Fatal("construction contacted Kubernetes")
	}
}
