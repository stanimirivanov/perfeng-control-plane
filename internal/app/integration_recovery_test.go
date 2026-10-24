package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type recoveryStub struct{ calls chan int }

func (s recoveryStub) RecoverIntegration(ctx context.Context, limit int) error {
	select {
	case s.calls <- limit:
	case <-ctx.Done():
		return ctx.Err()
	}
	return run.ErrUnavailable
}

func TestIntegrationRecoveryRetriesWhileReconciliationIsBusyAndStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := newInertRuntimeWorker()
	calls, events := make(chan int, 4), make(chan worker.Event, 4)
	w := integrationRecovery{next: next, store: recoveryStub{calls}, report: func(e worker.Event) {
		select {
		case events <- e:
		default:
		}
	}, every: time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	awaitStarted(t, next.started, "reconciliation")
	for range 2 {
		select {
		case limit := <-calls:
			if limit != 100 {
				t.Fatal("unbounded recovery", limit)
			}
		case <-ctx.Done():
			t.Fatal("recovery did not retry")
		}
		select {
		case e := <-events:
			if !errors.Is(e.Err, run.ErrUnavailable) || e.Operation != worker.OperationClaim {
				t.Fatal("lost failure report")
			}
		case <-ctx.Done():
			t.Fatal("missing recovery report")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery leaked worker")
	}
}

func TestIntegrationRecoveryPreservesUnexpectedWorkerExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := newInertRuntimeWorker()
	next.err = run.ErrUnavailable
	w := integrationRecovery{next: next, store: recoveryStub{make(chan int, 1)}, report: func(worker.Event) {}, every: time.Millisecond}
	if err := w.Run(ctx); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("worker failure lost", err)
	}
}
