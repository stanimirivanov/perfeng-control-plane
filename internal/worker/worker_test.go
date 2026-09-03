package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type reconcileFunc func(context.Context, run.Claim) (Result, error)

func (function reconcileFunc) Reconcile(ctx context.Context, claim run.Claim) (Result, error) {
	return function(ctx, claim)
}

type releasedClaim struct {
	lease run.Lease
	delay time.Duration
}

type workerStore struct {
	mu          sync.Mutex
	pending     []run.Claim
	active      map[string]run.Claim
	claimErr    error
	renew       func(run.Claim) (run.Claim, error)
	releaseErr  error
	claimLimits chan int
	releases    chan releasedClaim
}

func newWorkerStore(claims ...run.Claim) *workerStore {
	return &workerStore{
		pending:     claims,
		active:      make(map[string]run.Claim),
		claimLimits: make(chan int, 20),
		releases:    make(chan releasedClaim, 20),
	}
}

func (store *workerStore) ClaimRuns(
	ctx context.Context,
	workerID string,
	limit int,
	ttl time.Duration,
) ([]run.Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	select {
	case store.claimLimits <- limit:
	default:
	}
	if store.claimErr != nil {
		err := store.claimErr
		store.claimErr = nil

		return nil, err
	}

	count := min(limit, len(store.pending))
	claims := append([]run.Claim(nil), store.pending[:count]...)
	store.pending = store.pending[count:]
	for _, claim := range claims {
		store.active[claim.Run.ID] = claim
	}

	return claims, nil
}

func (store *workerStore) RenewClaim(
	ctx context.Context,
	lease run.Lease,
	ttl time.Duration,
) (run.Claim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	claim, exists := store.active[lease.RunID]
	if !exists || claim.Lease.Token != lease.Token {
		return run.Claim{}, run.ErrLeaseLost
	}
	if store.renew != nil {
		renewed, err := store.renew(claim)
		if err == nil {
			store.active[lease.RunID] = renewed
		}

		return renewed, err
	}

	return claim, nil
}

func (store *workerStore) ReleaseClaim(
	ctx context.Context,
	lease run.Lease,
	retryDelay time.Duration,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if store.releaseErr != nil {
		return store.releaseErr
	}
	claim, exists := store.active[lease.RunID]
	if !exists || claim.Lease.Token != lease.Token {
		return run.ErrLeaseLost
	}
	delete(store.active, lease.RunID)
	store.releases <- releasedClaim{lease: lease, delay: retryDelay}

	return nil
}

func (store *workerStore) AdvanceClaim(
	ctx context.Context,
	lease run.Lease,
	expectedRevision int64,
	change run.Change,
) (run.Run, error) {
	return run.Run{}, errors.New("unexpected AdvanceClaim call")
}

func testClaim(index int) run.Claim {
	runID := fmt.Sprintf("perf-20260903-120000-%08x", index)

	return run.Claim{
		Lease: run.Lease{
			RunID: runID, Principal: "principal-a", WorkerID: "worker-1",
			Token: fmt.Sprintf("%032x", index), ExpiresAt: time.Now().Add(time.Minute),
		},
		Run: run.Run{ID: runID, State: run.StateCreated, Revision: 1},
	}
}

func testWorker(
	t *testing.T,
	store run.ReconciliationStore,
	reconciler Reconciler,
	capacity int,
) (*Worker, <-chan Event) {
	t.Helper()

	events := make(chan Event, 20)
	config := DefaultConfig("worker-1")
	config.Capacity = capacity
	config.RenewEvery = 20 * time.Millisecond
	config.PollEvery = 10 * time.Millisecond
	config.FailureDelay = 5 * time.Second
	worker, err := New(store, reconciler, func(event Event) { events <- event }, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return worker, events
}

func runWorker(t *testing.T, worker *Worker) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	return cancel, done
}

func stopWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func receive[T any](t *testing.T, channel <-chan T, description string) T {
	t.Helper()

	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func TestWorkerBoundsConcurrencyAndReleases(t *testing.T) {
	store := newWorkerStore(testClaim(1), testClaim(2), testClaim(3))
	started := make(chan string, 3)
	finish := make(chan struct{}, 3)
	reconciler := reconcileFunc(func(_ context.Context, claim run.Claim) (Result, error) {
		started <- claim.Run.ID
		<-finish
		return Result{}, nil
	})
	worker, events := testWorker(t, store, reconciler, 2)
	cancel, done := runWorker(t, worker)

	receive(t, started, "first attempt")
	receive(t, started, "second attempt")
	select {
	case runID := <-started:
		t.Fatalf("third attempt %q started before capacity was available", runID)
	case <-time.After(50 * time.Millisecond):
	}
	finish <- struct{}{}
	if released := receive(t, store.releases, "first release"); released.delay != 0 {
		t.Errorf("release delay = %s, want 0", released.delay)
	}
	receive(t, started, "third attempt")
	finish <- struct{}{}
	finish <- struct{}{}
	for range 2 {
		if released := receive(t, store.releases, "remaining release"); released.delay != 0 {
			t.Errorf("release delay = %s, want 0", released.delay)
		}
	}
	stopWorker(t, cancel, done)

	if limit := receive(t, store.claimLimits, "claim limit"); limit != 2 {
		t.Errorf("first claim limit = %d, want 2", limit)
	}
	select {
	case event := <-events:
		t.Errorf("unexpected event: %+v", event)
	default:
	}
}

func TestWorkerRenewsAndInterruptsForCancellation(t *testing.T) {
	claim := testClaim(1)
	store := newWorkerStore(claim)
	store.renew = func(claim run.Claim) (run.Claim, error) {
		claim.Run.State = run.StateCancelling
		claim.Run.Revision++
		return claim, nil
	}
	causes := make(chan error, 1)
	reconciler := reconcileFunc(func(ctx context.Context, _ run.Claim) (Result, error) {
		<-ctx.Done()
		causes <- context.Cause(ctx)
		return Result{}, ctx.Err()
	})
	worker, events := testWorker(t, store, reconciler, 1)
	cancel, done := runWorker(t, worker)

	if cause := receive(t, causes, "cancellation cause"); !errors.Is(cause, ErrCancellationObserved) {
		t.Errorf("cancellation cause = %v", cause)
	}
	if released := receive(t, store.releases, "cancelling release"); released.delay != 0 {
		t.Errorf("release delay = %s, want 0", released.delay)
	}
	stopWorker(t, cancel, done)
	select {
	case event := <-events:
		t.Errorf("unexpected event: %+v", event)
	default:
	}
}

func TestWorkerDoesNotReleaseAfterShutdownOrLostRenewal(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		store := newWorkerStore(testClaim(1))
		started := make(chan struct{})
		causes := make(chan error, 1)
		reconciler := reconcileFunc(func(ctx context.Context, _ run.Claim) (Result, error) {
			close(started)
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return Result{}, ctx.Err()
		})
		worker, _ := testWorker(t, store, reconciler, 1)
		cancel, done := runWorker(t, worker)
		receive(t, started, "attempt start")
		cancel()
		if cause := receive(t, causes, "shutdown cause"); !errors.Is(cause, context.Canceled) {
			t.Errorf("shutdown cause = %v", cause)
		}
		stopWorker(t, func() {}, done)
		select {
		case released := <-store.releases:
			t.Errorf("unexpected release after shutdown: %+v", released)
		default:
		}
	})

	t.Run("lease lost", func(t *testing.T) {
		store := newWorkerStore(testClaim(1))
		store.renew = func(run.Claim) (run.Claim, error) {
			return run.Claim{}, run.ErrLeaseLost
		}
		causes := make(chan error, 1)
		reconciler := reconcileFunc(func(ctx context.Context, _ run.Claim) (Result, error) {
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return Result{}, ctx.Err()
		})
		worker, events := testWorker(t, store, reconciler, 1)
		cancel, done := runWorker(t, worker)
		if cause := receive(t, causes, "lease-loss cause"); !errors.Is(cause, run.ErrLeaseLost) {
			t.Errorf("lease-loss cause = %v", cause)
		}
		stopWorker(t, cancel, done)
		select {
		case released := <-store.releases:
			t.Errorf("unexpected release after lease loss: %+v", released)
		default:
		}
		select {
		case event := <-events:
			t.Errorf("unexpected event: %+v", event)
		default:
		}
	})

	t.Run("renewal unavailable", func(t *testing.T) {
		store := newWorkerStore(testClaim(1))
		store.renew = func(run.Claim) (run.Claim, error) {
			return run.Claim{}, run.ErrUnavailable
		}
		causes := make(chan error, 1)
		reconciler := reconcileFunc(func(ctx context.Context, _ run.Claim) (Result, error) {
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return Result{}, ctx.Err()
		})
		worker, events := testWorker(t, store, reconciler, 1)
		cancel, done := runWorker(t, worker)
		if cause := receive(t, causes, "renewal-failure cause"); !errors.Is(cause, run.ErrUnavailable) {
			t.Errorf("renewal-failure cause = %v", cause)
		}
		event := receive(t, events, "renewal event")
		if event.RunID != testClaim(1).Run.ID || event.Operation != OperationRenew ||
			!errors.Is(event.Err, run.ErrUnavailable) {
			t.Errorf("renewal event = %+v", event)
		}
		stopWorker(t, cancel, done)
		select {
		case released := <-store.releases:
			t.Errorf("unexpected release after renewal failure: %+v", released)
		default:
		}
	})
}

func TestWorkerReportsReconciliationFailuresAndUsesSafeDelay(t *testing.T) {
	tests := []struct {
		name     string
		result   Result
		err      error
		reported error
	}{
		{name: "reconciler error", err: errors.New("attempt failed"), reported: errors.New("attempt failed")},
		{name: "invalid retry delay", result: Result{RetryAfter: 500 * time.Millisecond}, reported: run.ErrValidation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := testClaim(1)
			store := newWorkerStore(claim)
			reconciler := reconcileFunc(func(context.Context, run.Claim) (Result, error) {
				return test.result, test.err
			})
			worker, events := testWorker(t, store, reconciler, 1)
			cancel, done := runWorker(t, worker)

			event := receive(t, events, "reconciliation event")
			if event.RunID != claim.Run.ID || event.Operation != OperationReconcile ||
				event.Err.Error() != test.reported.Error() {
				t.Errorf("event = %+v", event)
			}
			if released := receive(t, store.releases, "failure release"); released.delay != 5*time.Second {
				t.Errorf("release delay = %s, want 5s", released.delay)
			}
			stopWorker(t, cancel, done)
		})
	}
}

func TestWorkerReportsClaimAndReleaseFailures(t *testing.T) {
	claim := testClaim(1)
	store := newWorkerStore(claim)
	store.claimErr = run.ErrUnavailable
	store.releaseErr = run.ErrUnavailable
	reconciler := reconcileFunc(func(context.Context, run.Claim) (Result, error) {
		return Result{}, nil
	})
	worker, events := testWorker(t, store, reconciler, 1)
	cancel, done := runWorker(t, worker)

	claimEvent := receive(t, events, "claim event")
	if claimEvent.RunID != "" || claimEvent.Operation != OperationClaim ||
		!errors.Is(claimEvent.Err, run.ErrUnavailable) {
		t.Errorf("claim event = %+v", claimEvent)
	}
	releaseEvent := receive(t, events, "release event")
	if releaseEvent.RunID != claim.Run.ID || releaseEvent.Operation != OperationRelease ||
		!errors.Is(releaseEvent.Err, run.ErrUnavailable) {
		t.Errorf("release event = %+v", releaseEvent)
	}
	stopWorker(t, cancel, done)
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	store := newWorkerStore()
	reconciler := reconcileFunc(func(context.Context, run.Claim) (Result, error) { return Result{}, nil })
	report := func(Event) {}
	valid := DefaultConfig("worker-1")

	tests := []struct {
		name       string
		store      run.ReconciliationStore
		reconciler Reconciler
		report     func(Event)
		config     Config
	}{
		{name: "store", reconciler: reconciler, report: report, config: valid},
		{name: "reconciler", store: store, report: report, config: valid},
		{name: "report", store: store, reconciler: reconciler, config: valid},
	}
	invalidConfigs := []Config{
		func() Config { config := valid; config.WorkerID = ""; return config }(),
		func() Config { config := valid; config.Capacity = 0; return config }(),
		func() Config { config := valid; config.LeaseTTL = time.Second; return config }(),
		func() Config { config := valid; config.RenewEvery = 16 * time.Second; return config }(),
		func() Config { config := valid; config.PollEvery = 0; return config }(),
		func() Config { config := valid; config.FailureDelay = time.Millisecond; return config }(),
	}
	for index, config := range invalidConfigs {
		tests = append(tests, struct {
			name       string
			store      run.ReconciliationStore
			reconciler Reconciler
			report     func(Event)
			config     Config
		}{name: fmt.Sprintf("config %d", index), store: store, reconciler: reconciler, report: report, config: config})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.store, test.reconciler, test.report, test.config); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("New() error = %v, want validation error", err)
			}
		})
	}
}
