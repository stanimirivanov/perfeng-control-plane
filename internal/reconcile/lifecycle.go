// Package reconcile maps trusted execution observations to run lifecycle work.
package reconcile

import (
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrStateNotHandled means the Run is outside the Kubernetes execution portion
// of the lifecycle and must be handled by another reconciler stage.
var ErrStateNotHandled = errors.New("run state is not handled by Kubernetes execution reconciliation")

// Action identifies the side effect required by a lifecycle decision.
type Action string

const (
	// ActionWait makes no external or lifecycle mutation during this attempt.
	ActionWait Action = "wait"
	// ActionStop requests deletion of the exact persisted execution.
	ActionStop Action = "stop"
	// ActionConfirmStop verifies that no owned Pods remain after Job deletion.
	ActionConfirmStop Action = "confirm-stop"
	// ActionAdvance applies Decision.Change through the current lease.
	ActionAdvance Action = "advance"
)

// Decision contains either a wait, a stop request, or one validated lifecycle
// change. Callers must apply advances through their current reconciliation lease.
type Decision struct {
	Action Action
	Change run.Change
}

// DecideBoundExecution interprets an observation of the exact, durably persisted
// Kubernetes execution. It does not interpret test results or artifact quality.
func DecideBoundExecution(state run.State, observation kubernetes.Observation) (Decision, error) {
	if !validPhase(observation.Phase) {
		return Decision{}, run.ErrValidation
	}
	if state == run.StateCancelling {
		if observation.Phase == kubernetes.JobAbsent {
			return Decision{Action: ActionConfirmStop}, nil
		}

		return Decision{Action: ActionStop}, nil
	}
	if state != run.StateProvisioning && state != run.StateWarmingUp && state != run.StateRunning {
		return Decision{}, ErrStateNotHandled
	}
	if observation.Phase == kubernetes.JobAbsent {
		return infrastructureFailure("Kubernetes Job disappeared before artifact collection"), nil
	}
	if observation.Deleting {
		return infrastructureFailure("Kubernetes Job deletion was not requested by the run"), nil
	}

	switch state {
	case run.StateProvisioning:
		if observation.Phase == kubernetes.JobPending {
			return Decision{Action: ActionWait}, nil
		}

		return advance(run.Change{State: run.StateRunning}), nil
	case run.StateWarmingUp:
		if !terminal(observation.Phase) {
			return Decision{Action: ActionWait}, nil
		}

		return advance(run.Change{State: run.StateCollecting}), nil
	case run.StateRunning:
		if !terminal(observation.Phase) {
			return Decision{Action: ActionWait}, nil
		}

		return advance(run.Change{State: run.StateCollecting}), nil
	default:
		return Decision{}, ErrStateNotHandled
	}
}

func infrastructureFailure(message string) Decision {
	return advance(run.Change{
		State: run.StateInfrastructureFailure,
		Failure: &run.Failure{
			Code:    run.FailureCodeInfrastructureError,
			Message: message,
		},
	})
}

func advance(change run.Change) Decision {
	return Decision{Action: ActionAdvance, Change: change}
}

func terminal(phase kubernetes.JobPhase) bool {
	return phase == kubernetes.JobSucceeded || phase == kubernetes.JobFailed
}

func validPhase(phase kubernetes.JobPhase) bool {
	switch phase {
	case kubernetes.JobAbsent,
		kubernetes.JobPending,
		kubernetes.JobRunning,
		kubernetes.JobSucceeded,
		kubernetes.JobFailed:
		return true
	default:
		return false
	}
}
