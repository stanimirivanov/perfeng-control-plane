// Package worker coordinates bounded, lease-renewed reconciliation attempts.
package worker

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrCancellationObserved is the cancellation cause delivered to a reconciler
// when lease renewal reveals that its Run moved to CANCELLING.
var ErrCancellationObserved = errors.New("run cancellation observed during reconciliation")

// Operation identifies the worker boundary that produced a reported error.
type Operation string

const (
	OperationClaim     Operation = "claim"
	OperationRenew     Operation = "renew"
	OperationReconcile Operation = "reconcile"
	OperationRelease   Operation = "release"
)

// Event reports an operational failure without exposing a lease capability.
type Event struct {
	RunID     string
	Operation Operation
	Err       error
}

// Result controls when a nonterminal Run becomes eligible after one attempt.
type Result struct {
	RetryAfter time.Duration
}

// Reconciler performs one bounded attempt. It must stop promptly when ctx is
// cancelled and must not retain or log the Claim's lease token.
type Reconciler interface {
	Reconcile(context.Context, run.Claim) (Result, error)
}

// Config defines worker concurrency and lease timing.
type Config struct {
	WorkerID     string
	Capacity     int
	LeaseTTL     time.Duration
	RenewEvery   time.Duration
	PollEvery    time.Duration
	FailureDelay time.Duration
}

// DefaultConfig returns conservative timings for a worker identity.
func DefaultConfig(workerID string) Config {
	return Config{
		WorkerID:     workerID,
		Capacity:     4,
		LeaseTTL:     30 * time.Second,
		RenewEvery:   10 * time.Second,
		PollEvery:    time.Second,
		FailureDelay: 5 * time.Second,
	}
}

// Worker discovers active Runs and keeps at most Config.Capacity attempts active.
type Worker struct {
	store      run.ReconciliationStore
	reconciler Reconciler
	report     func(Event)
	config     Config
}

// New validates all dependencies and timing bounds.
func New(
	store run.ReconciliationStore,
	reconciler Reconciler,
	report func(Event),
	config Config,
) (*Worker, error) {
	if store == nil || reconciler == nil || report == nil ||
		!run.ValidClaimOptions(config.WorkerID, config.Capacity, config.LeaseTTL) ||
		config.RenewEvery <= 0 || config.RenewEvery > config.LeaseTTL/2 ||
		config.PollEvery <= 0 || config.PollEvery > 5*time.Minute ||
		!run.ValidRetryDelay(config.FailureDelay) {
		return nil, run.ErrValidation
	}

	return &Worker{store: store, reconciler: reconciler, report: report, config: config}, nil
}

type completion struct {
	runID  string
	events []Event
}

// Run processes claims until ctx is cancelled. Shutdown cancels active attempts,
// stops their renewal and waits for reconcilers to return. It deliberately leaves
// their leases to expire rather than asserting that external effects stopped.
func (worker *Worker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()

	completed := make(chan completion, worker.config.Capacity)
	active := make(map[string]bool, worker.config.Capacity)

	for {
		var poll <-chan time.Time
		if len(active) < worker.config.Capacity {
			poll = timer.C
		}

		select {
		case <-ctx.Done():
			for range active {
				result := <-completed
				delete(active, result.runID)
			}

			return ctx.Err()
		case result := <-completed:
			delete(active, result.runID)
			worker.reportEvents(result.events)
			resetTimer(timer, 0)
		case <-poll:
			if err := worker.startAvailable(ctx, active, completed); err != nil && ctx.Err() == nil {
				worker.report(Event{Operation: OperationClaim, Err: err})
			}
			resetTimer(timer, worker.config.PollEvery)
		}
	}
}

func (worker *Worker) startAvailable(
	ctx context.Context,
	active map[string]bool,
	completed chan<- completion,
) error {
	claims, err := worker.store.ClaimRuns(
		ctx,
		worker.config.WorkerID,
		worker.config.Capacity-len(active),
		worker.config.LeaseTTL,
	)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		if !validClaim(claim) || active[claim.Run.ID] {
			worker.report(Event{
				RunID: claim.Run.ID, Operation: OperationClaim, Err: run.ErrValidation,
			})

			continue
		}
		active[claim.Run.ID] = true
		go func() { completed <- worker.process(ctx, claim) }()
	}

	return nil
}

func (worker *Worker) process(parent context.Context, claim run.Claim) completion {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	type reconcileResult struct {
		result Result
		err    error
	}
	done := make(chan reconcileResult, 1)
	go func(attempt run.Claim) {
		result, err := worker.reconciler.Reconcile(ctx, attempt)
		done <- reconcileResult{result: result, err: err}
	}(claim)

	timer := time.NewTimer(worker.config.RenewEvery)
	defer timer.Stop()

	for {
		select {
		case result := <-done:
			if parent.Err() != nil {
				return completion{runID: claim.Run.ID}
			}

			return worker.finish(parent, claim, result.result, result.err)
		case <-timer.C:
			renewed, err := worker.store.RenewClaim(ctx, claim.Lease, worker.config.LeaseTTL)
			if err != nil {
				cancel(err)
				<-done
				if errors.Is(err, run.ErrLeaseLost) || parent.Err() != nil {
					return completion{runID: claim.Run.ID}
				}

				return completion{runID: claim.Run.ID, events: []Event{{
					RunID: claim.Run.ID, Operation: OperationRenew, Err: err,
				}}}
			}
			if claim.Run.State != run.StateCancelling && renewed.Run.State == run.StateCancelling {
				cancel(ErrCancellationObserved)
				<-done

				return worker.release(parent, renewed, 0)
			}
			claim = renewed
			resetTimer(timer, worker.config.RenewEvery)
		case <-parent.Done():
			cancel(parent.Err())
			<-done

			return completion{runID: claim.Run.ID}
		}
	}
}

func (worker *Worker) finish(
	ctx context.Context,
	claim run.Claim,
	result Result,
	reconcileErr error,
) completion {
	if errors.Is(reconcileErr, run.ErrLeaseLost) {
		return completion{runID: claim.Run.ID}
	}

	delay := result.RetryAfter
	events := make([]Event, 0, 2)
	if reconcileErr != nil {
		delay = worker.config.FailureDelay
		events = append(events, Event{
			RunID: claim.Run.ID, Operation: OperationReconcile, Err: reconcileErr,
		})
	} else if !run.ValidRetryDelay(delay) {
		delay = worker.config.FailureDelay
		events = append(events, Event{
			RunID: claim.Run.ID, Operation: OperationReconcile, Err: run.ErrValidation,
		})
	}

	released := worker.release(ctx, claim, delay)
	released.events = append(events, released.events...)

	return released
}

func (worker *Worker) release(ctx context.Context, claim run.Claim, delay time.Duration) completion {
	result := completion{runID: claim.Run.ID}
	if err := worker.store.ReleaseClaim(ctx, claim.Lease, delay); err != nil &&
		!errors.Is(err, run.ErrLeaseLost) {
		result.events = []Event{{RunID: claim.Run.ID, Operation: OperationRelease, Err: err}}
	}

	return result
}

func (worker *Worker) reportEvents(events []Event) {
	for _, event := range events {
		worker.report(event)
	}
}

func validClaim(claim run.Claim) bool {
	return claim.Lease.Valid() &&
		claim.Run.ID == claim.Lease.RunID &&
		claim.Run.State != ""
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}
