package reconcile

import (
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestDecideBoundExecution(t *testing.T) {
	tests := []struct {
		name        string
		state       run.State
		observation kubernetes.Observation
		action      Action
		next        run.State
	}{
		{"provisioning pending", run.StateProvisioning, observed(kubernetes.JobPending), ActionWait, ""},
		{"provisioning running", run.StateProvisioning, observed(kubernetes.JobRunning), ActionAdvance, run.StateRunning},
		{"provisioning succeeded", run.StateProvisioning, observed(kubernetes.JobSucceeded), ActionAdvance, run.StateRunning},
		{"provisioning failed", run.StateProvisioning, observed(kubernetes.JobFailed), ActionAdvance, run.StateRunning},
		{"warmup pending", run.StateWarmingUp, observed(kubernetes.JobPending), ActionWait, ""},
		{"warmup running", run.StateWarmingUp, observed(kubernetes.JobRunning), ActionWait, ""},
		{"warmup succeeded", run.StateWarmingUp, observed(kubernetes.JobSucceeded), ActionAdvance, run.StateCollecting},
		{"warmup failed", run.StateWarmingUp, observed(kubernetes.JobFailed), ActionAdvance, run.StateCollecting},
		{"running pending", run.StateRunning, observed(kubernetes.JobPending), ActionWait, ""},
		{"running active", run.StateRunning, observed(kubernetes.JobRunning), ActionWait, ""},
		{"running succeeded", run.StateRunning, observed(kubernetes.JobSucceeded), ActionAdvance, run.StateCollecting},
		{"running failed", run.StateRunning, observed(kubernetes.JobFailed), ActionAdvance, run.StateCollecting},
		{"cancelling pending", run.StateCancelling, observed(kubernetes.JobPending), ActionStop, ""},
		{"cancelling running", run.StateCancelling, observed(kubernetes.JobRunning), ActionStop, ""},
		{"cancelling succeeded", run.StateCancelling, observed(kubernetes.JobSucceeded), ActionStop, ""},
		{"cancelling failed", run.StateCancelling, observed(kubernetes.JobFailed), ActionStop, ""},
		{"cancelling absent", run.StateCancelling, observed(kubernetes.JobAbsent), ActionAdvance, run.StateAborted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := DecideBoundExecution(test.state, test.observation)
			if err != nil {
				t.Fatalf("DecideBoundExecution() error = %v", err)
			}
			if decision.Action != test.action || decision.Change.State != test.next {
				t.Fatalf("decision = %+v, want action %q and state %q", decision, test.action, test.next)
			}
			if decision.Change.State == run.StateTestFailure {
				t.Fatal("Kubernetes status was interpreted as a test result")
			}
			if decision.Action == ActionAdvance {
				current := run.Run{State: test.state, Revision: 1, UpdatedAt: time.Now()}
				if _, err := current.Transition(1, decision.Change, time.Now()); err != nil {
					t.Fatalf("decision is not a valid lifecycle transition: %v", err)
				}
			}
		})
	}
}

func TestDecideBoundExecutionTreatsUnexpectedDisappearanceAsInfrastructureFailure(t *testing.T) {
	for _, state := range []run.State{run.StateProvisioning, run.StateWarmingUp, run.StateRunning} {
		for _, observation := range []kubernetes.Observation{
			observed(kubernetes.JobAbsent),
			{Phase: kubernetes.JobRunning, Deleting: true},
		} {
			decision, err := DecideBoundExecution(state, observation)
			if err != nil {
				t.Fatalf("state %s: %v", state, err)
			}
			failure := decision.Change.Failure
			if decision.Action != ActionAdvance ||
				decision.Change.State != run.StateInfrastructureFailure ||
				failure == nil || failure.Code != run.FailureCodeInfrastructureError ||
				failure.Message == "" {
				t.Errorf("state %s observation %+v produced %+v", state, observation, decision)
			}
			current := run.Run{State: state, Revision: 1, UpdatedAt: time.Now()}
			if _, err := current.Transition(1, decision.Change, time.Now()); err != nil {
				t.Errorf("state %s produced invalid transition: %v", state, err)
			}
		}
	}
}

func TestDecideBoundExecutionRejectsUnsupportedInput(t *testing.T) {
	for _, state := range []run.State{
		run.StateCreated,
		run.StateValidating,
		run.StateCollecting,
		run.StateAnalyzing,
		run.StateReporting,
		run.StateCompleted,
		run.StateInvalid,
		run.StateAborted,
		run.StateInfrastructureFailure,
		run.StateTestFailure,
	} {
		if _, err := DecideBoundExecution(state, observed(kubernetes.JobRunning)); !errors.Is(err, ErrStateNotHandled) {
			t.Errorf("state %s error = %v", state, err)
		}
	}
	if _, err := DecideBoundExecution(run.StateRunning, kubernetes.Observation{Phase: "UNKNOWN"}); !errors.Is(err, run.ErrValidation) {
		t.Errorf("invalid phase error = %v", err)
	}
}

func observed(phase kubernetes.JobPhase) kubernetes.Observation {
	return kubernetes.Observation{Phase: phase}
}
