package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type routeStage struct {
	name   string
	calls  *[]string
	result worker.Result
	err    error
}

func (stage routeStage) Reconcile(context.Context, run.Claim) (worker.Result, error) {
	*stage.calls = append(*stage.calls, stage.name)

	return stage.result, stage.err
}

func routerFixture(t *testing.T) (*Router, *advancingStore, *[]string) {
	t.Helper()

	store := &advancingStore{}
	calls := []string{}
	result := worker.Result{RetryAfter: time.Second}
	router, err := NewRouter(
		store,
		routeStage{name: "validation", calls: &calls, result: result},
		routeStage{name: "provisioning", calls: &calls, result: result},
		routeStage{name: "bound", calls: &calls, result: result},
		5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	return router, store, &calls
}

func TestRouterRoutesEveryActiveState(t *testing.T) {
	tests := []struct {
		state run.State
		stage string
	}{
		{run.StateValidating, "validation"},
		{run.StateProvisioning, "provisioning"},
		{run.StateWarmingUp, "bound"},
		{run.StateRunning, "bound"},
		{run.StateCancelling, "bound"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			router, store, calls := routerFixture(t)
			result, err := router.Reconcile(context.Background(), boundClaim(test.state))
			if err != nil || result.RetryAfter != time.Second ||
				len(*calls) != 1 || (*calls)[0] != test.stage || store.calls != 0 {
				t.Fatalf("route = %v, %+v, %v", *calls, result, err)
			}
		})
	}
}

func TestRouterAdvancesCreatedAndDefersPostExecutionStates(t *testing.T) {
	router, store, calls := routerFixture(t)
	claim := boundClaim(run.StateCreated)
	if _, err := router.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.change.State != run.StateValidating ||
		store.revision != claim.Run.Revision || len(*calls) != 0 {
		t.Fatalf("CREATED effects: %+v, %v", store, *calls)
	}

	for _, state := range []run.State{run.StateCollecting, run.StateAnalyzing, run.StateReporting} {
		router, store, calls = routerFixture(t)
		result, err := router.Reconcile(context.Background(), boundClaim(state))
		if err != nil || result.RetryAfter != 5*time.Minute || store.calls != 0 || len(*calls) != 0 {
			t.Fatalf("state %s = %+v, %v", state, result, err)
		}
	}
}

func TestRouterRejectsTerminalUnknownAndInvalidClaims(t *testing.T) {
	for _, state := range []run.State{
		run.StateCompleted, run.StateInvalid, run.StateAborted,
		run.StateInfrastructureFailure, run.StateTestFailure, "UNKNOWN",
	} {
		router, store, calls := routerFixture(t)
		if _, err := router.Reconcile(context.Background(), boundClaim(state)); !errors.Is(err, ErrStateNotHandled) {
			t.Fatalf("state %s error = %v", state, err)
		}
		if store.calls != 0 || len(*calls) != 0 {
			t.Fatalf("state %s accessed a stage", state)
		}
	}
	router, store, calls := routerFixture(t)
	claim := boundClaim(run.StateCreated)
	claim.Run.ID = "different"
	if _, err := router.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if store.calls != 0 || len(*calls) != 0 {
		t.Fatal("invalid claim accessed a stage")
	}
}

func TestRouterPreservesRevisionAndStageErrors(t *testing.T) {
	router, store, _ := routerFixture(t)
	store.err = run.ErrRevision
	if _, err := router.Reconcile(context.Background(), boundClaim(run.StateCreated)); err != nil {
		t.Fatalf("revision race error = %v", err)
	}
	store.err = run.ErrLeaseLost
	if _, err := router.Reconcile(context.Background(), boundClaim(run.StateCreated)); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("lease loss error = %v", err)
	}

	calls := []string{}
	router.validation = routeStage{name: "validation", calls: &calls, err: run.ErrUnavailable}
	if _, err := router.Reconcile(context.Background(), boundClaim(run.StateValidating)); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("stage error = %v", err)
	}
}

func TestRouterValidatesDependenciesAndDelay(t *testing.T) {
	router, _, _ := routerFixture(t)
	for _, delay := range []time.Duration{0, time.Millisecond, 5*time.Minute + time.Second} {
		if _, err := NewRouter(router.store, router.validation, router.provisioning, router.bound, delay); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("delay %s error = %v", delay, err)
		}
	}
	for _, missing := range []string{"store", "validation", "provisioning", "bound"} {
		candidate := *router
		switch missing {
		case "store":
			candidate.store = nil
		case "validation":
			candidate.validation = nil
		case "provisioning":
			candidate.provisioning = nil
		case "bound":
			candidate.bound = nil
		}
		if _, err := NewRouter(
			candidate.store,
			candidate.validation,
			candidate.provisioning,
			candidate.bound,
			candidate.deferredRetry,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("missing %s error = %v", missing, err)
		}
	}
}
