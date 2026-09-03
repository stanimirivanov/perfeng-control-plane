package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type boundStore struct {
	execution  kubernetes.Execution
	found      bool
	getErr     error
	advanceErr error
	changes    []run.Change
	revisions  []int64
}

func (store *boundStore) GetExecution(
	ctx context.Context,
	lease run.Lease,
) (kubernetes.Execution, bool, error) {
	if err := ctx.Err(); err != nil {
		return kubernetes.Execution{}, false, err
	}

	return store.execution, store.found, store.getErr
}

func (store *boundStore) AdvanceClaim(
	ctx context.Context,
	lease run.Lease,
	revision int64,
	change run.Change,
) (run.Run, error) {
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	store.changes = append(store.changes, change)
	store.revisions = append(store.revisions, revision)

	return run.Run{}, store.advanceErr
}

type executionController struct {
	mu           sync.Mutex
	observations []kubernetes.Observation
	observeErr   error
	stopErr      error
	confirmErr   error
	stoppedState bool
	observed     int
	stopped      int
	confirmed    int
}

func (controller *executionController) ObserveJob(
	ctx context.Context,
	execution kubernetes.Execution,
) (kubernetes.Observation, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return kubernetes.Observation{}, err
	}
	controller.observed++
	if controller.observeErr != nil {
		return kubernetes.Observation{}, controller.observeErr
	}
	if len(controller.observations) == 0 {
		return kubernetes.Observation{}, errors.New("no test observation configured")
	}
	index := min(controller.observed-1, len(controller.observations)-1)

	return controller.observations[index], nil
}

func (controller *executionController) RequestJobStop(
	ctx context.Context,
	execution kubernetes.Execution,
) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	controller.stopped++

	return controller.stopErr
}

func (controller *executionController) ConfirmExecutionStopped(
	ctx context.Context,
	execution kubernetes.Execution,
) (bool, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}
	controller.confirmed++

	return controller.stoppedState, controller.confirmErr
}

func (controller *executionController) counts() (int, int, int) {
	controller.mu.Lock()
	defer controller.mu.Unlock()

	return controller.observed, controller.stopped, controller.confirmed
}

func boundClaim(state run.State) run.Claim {
	const runID = "perf-20260903-120000-12345678"

	return run.Claim{
		Lease: run.Lease{
			RunID: runID, Principal: "principal-a", WorkerID: "worker-1",
			Token: strings.Repeat("a", 32), ExpiresAt: time.Now().Add(time.Minute),
		},
		Run: run.Run{ID: runID, State: state, Revision: 7},
	}
}

func boundExecution() kubernetes.Execution {
	const runID = "perf-20260903-120000-12345678"

	return kubernetes.Execution{
		RunID: runID, Namespace: "perf-runs", JobName: runID,
		UID:        types.UID("a43fbf7a-0fe9-472a-a42d-6955ec437252"),
		SpecSHA256: strings.Repeat("b", 64),
	}
}

func newBoundReconciler(
	t *testing.T,
	store *boundStore,
	controller *executionController,
) *BoundExecutionReconciler {
	t.Helper()

	config := DefaultBoundExecutionConfig()
	config.CancellationPollEvery = time.Millisecond
	reconciler, err := NewBoundExecutionReconciler(store, controller, controller, config)
	if err != nil {
		t.Fatalf("NewBoundExecutionReconciler() error = %v", err)
	}

	return reconciler
}

func TestBoundExecutionReconcilerWaitsForPendingJob(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true}
	controller := &executionController{observations: []kubernetes.Observation{
		{Phase: kubernetes.JobPending},
	}}
	reconciler := newBoundReconciler(t, store, controller)

	result, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateProvisioning))
	if err != nil || result.RetryAfter != 5*time.Second {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	if len(store.changes) != 0 {
		t.Fatalf("pending Job produced lifecycle changes: %+v", store.changes)
	}
}

func TestBoundExecutionReconcilerAppliesLifecycleDecisions(t *testing.T) {
	tests := []struct {
		name  string
		state run.State
		job   kubernetes.Observation
		next  run.State
	}{
		{"started", run.StateProvisioning, kubernetes.Observation{Phase: kubernetes.JobRunning}, run.StateRunning},
		{"succeeded", run.StateRunning, kubernetes.Observation{Phase: kubernetes.JobSucceeded}, run.StateCollecting},
		{"failed", run.StateRunning, kubernetes.Observation{Phase: kubernetes.JobFailed}, run.StateCollecting},
		{"disappeared", run.StateRunning, kubernetes.Observation{Phase: kubernetes.JobAbsent}, run.StateInfrastructureFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &boundStore{execution: boundExecution(), found: true}
			controller := &executionController{observations: []kubernetes.Observation{test.job}}
			reconciler := newBoundReconciler(t, store, controller)
			claim := boundClaim(test.state)

			result, err := reconciler.Reconcile(context.Background(), claim)
			if err != nil || result.RetryAfter != 0 {
				t.Fatalf("Reconcile() = %+v, %v", result, err)
			}
			if len(store.changes) != 1 || store.changes[0].State != test.next ||
				len(store.revisions) != 1 || store.revisions[0] != claim.Run.Revision {
				t.Fatalf("changes = %+v, revisions = %v", store.changes, store.revisions)
			}
		})
	}
}

func TestBoundExecutionReconcilerWaitsForCancellationCompletion(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true}
	controller := &executionController{observations: []kubernetes.Observation{
		{Phase: kubernetes.JobRunning},
		{Phase: kubernetes.JobRunning, Deleting: true},
		{Phase: kubernetes.JobAbsent},
	}, stoppedState: true}
	reconciler := newBoundReconciler(t, store, controller)

	result, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateCancelling))
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	observed, stopped, confirmed := controller.counts()
	if observed != 3 || stopped != 1 || confirmed != 1 {
		t.Fatalf("observed %d times, stopped %d times, and confirmed %d times", observed, stopped, confirmed)
	}
	if len(store.changes) != 1 || store.changes[0].State != run.StateAborted {
		t.Fatalf("cancellation changes = %+v", store.changes)
	}
}

func TestBoundExecutionReconcilerCancellationHonorsContext(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true}
	controller := &executionController{observations: []kubernetes.Observation{
		{Phase: kubernetes.JobRunning},
	}}
	reconciler := newBoundReconciler(t, store, controller)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	if _, err := reconciler.Reconcile(ctx, boundClaim(run.StateCancelling)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reconcile() error = %v", err)
	}
	_, stopped, _ := controller.counts()
	if stopped != 1 || len(store.changes) != 0 {
		t.Fatalf("stops = %d, changes = %+v", stopped, store.changes)
	}
}

func TestBoundExecutionReconcilerDoesNotAbortWhileOwnedPodsRemain(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true}
	controller := &executionController{
		observations: []kubernetes.Observation{{Phase: kubernetes.JobAbsent}},
	}
	reconciler := newBoundReconciler(t, store, controller)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	if _, err := reconciler.Reconcile(ctx, boundClaim(run.StateCancelling)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reconcile() error = %v", err)
	}
	observed, stopped, confirmed := controller.counts()
	if observed < 1 || stopped != 0 || confirmed < 1 || len(store.changes) != 0 {
		t.Fatalf(
			"observed %d times, stopped %d times, confirmed %d times, changes %+v",
			observed,
			stopped,
			confirmed,
			store.changes,
		)
	}
}

func TestBoundExecutionReconcilerPreservesBoundaryErrors(t *testing.T) {
	tests := []struct {
		name       string
		store      *boundStore
		controller *executionController
		state      run.State
		want       error
	}{
		{
			name: "lookup", store: &boundStore{getErr: run.ErrUnavailable},
			controller: &executionController{}, state: run.StateRunning, want: run.ErrUnavailable,
		},
		{
			name: "missing", store: &boundStore{},
			controller: &executionController{}, state: run.StateRunning, want: ErrExecutionNotBound,
		},
		{
			name: "observe", store: &boundStore{execution: boundExecution(), found: true},
			controller: &executionController{observeErr: run.ErrUnavailable},
			state:      run.StateRunning, want: run.ErrUnavailable,
		},
		{
			name: "stop", store: &boundStore{execution: boundExecution(), found: true},
			controller: &executionController{
				observations: []kubernetes.Observation{{Phase: kubernetes.JobRunning}},
				stopErr:      kubernetes.ErrJobConflict,
			},
			state: run.StateCancelling, want: kubernetes.ErrJobConflict,
		},
		{
			name: "confirm stop", store: &boundStore{execution: boundExecution(), found: true},
			controller: &executionController{
				observations: []kubernetes.Observation{{Phase: kubernetes.JobAbsent}},
				confirmErr:   run.ErrUnavailable,
			},
			state: run.StateCancelling, want: run.ErrUnavailable,
		},
		{
			name: "advance", store: &boundStore{
				execution: boundExecution(), found: true, advanceErr: run.ErrUnavailable,
			},
			controller: &executionController{
				observations: []kubernetes.Observation{{Phase: kubernetes.JobSucceeded}},
			},
			state: run.StateRunning, want: run.ErrUnavailable,
		},
		{
			name: "state", store: &boundStore{execution: boundExecution(), found: true},
			controller: &executionController{
				observations: []kubernetes.Observation{{Phase: kubernetes.JobRunning}},
			},
			state: run.StateCollecting, want: ErrStateNotHandled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reconciler := newBoundReconciler(t, test.store, test.controller)
			if _, err := reconciler.Reconcile(context.Background(), boundClaim(test.state)); !errors.Is(err, test.want) {
				t.Fatalf("Reconcile() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBoundExecutionReconcilerRediscoverAfterRevisionRace(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true, advanceErr: run.ErrRevision}
	controller := &executionController{observations: []kubernetes.Observation{
		{Phase: kubernetes.JobSucceeded},
	}}
	reconciler := newBoundReconciler(t, store, controller)

	result, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateRunning))
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
}

func TestBoundExecutionReconcilerValidatesDependenciesAndClaims(t *testing.T) {
	store := &boundStore{execution: boundExecution(), found: true}
	controller := &executionController{}
	valid := DefaultBoundExecutionConfig()
	for name, test := range map[string]struct {
		store      BoundExecutionStore
		controller ExecutionController
		verifier   TerminationVerifier
		config     BoundExecutionConfig
	}{
		"store":      {controller: controller, verifier: controller, config: valid},
		"controller": {store: store, verifier: controller, config: valid},
		"verifier":   {store: store, controller: controller, config: valid},
		"retry": {
			store: store, controller: controller, verifier: controller,
			config: BoundExecutionConfig{RetryAfter: time.Millisecond, CancellationPollEvery: time.Second},
		},
		"cancel poll": {
			store: store, controller: controller, verifier: controller,
			config: BoundExecutionConfig{RetryAfter: time.Second},
		},
		"cancel poll maximum": {
			store: store, controller: controller, verifier: controller,
			config: BoundExecutionConfig{
				RetryAfter: time.Second, CancellationPollEvery: 5*time.Minute + time.Nanosecond,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewBoundExecutionReconciler(
				test.store,
				test.controller,
				test.verifier,
				test.config,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("NewBoundExecutionReconciler() error = %v", err)
			}
		})
	}

	reconciler := newBoundReconciler(t, store, controller)
	claim := boundClaim(run.StateRunning)
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid claim error = %v", err)
	}

	store.execution.RunID = "perf-20260903-120000-87654321"
	claim = boundClaim(run.StateRunning)
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("mismatched execution error = %v", err)
	}
}
