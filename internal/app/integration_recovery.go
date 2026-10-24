package app

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// integrationRecovery runs independently of reconciliation capacity and HTTP
// traffic. Database state is the queue; restarting resumes overdue work.
type integrationRecovery struct {
	next   ReconciliationWorker
	store  integration.RecoveryStore
	report func(worker.Event)
	every  time.Duration
}

// Run retries recovery failures until shutdown or the supervised worker exits.
func (w integrationRecovery) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		err := w.next.Run(ctx)
		cancel()
		done <- err
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			return <-done
		case <-timer.C:
			if err := w.store.RecoverIntegration(ctx, 100); err != nil && ctx.Err() == nil {
				w.report(worker.Event{Operation: worker.OperationClaim, Err: err})
			}
			timer.Reset(w.every)
		}
	}
}
