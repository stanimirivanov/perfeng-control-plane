package run

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func failureFor(state string) *Failure {
	switch state {
	case "INVALID":
		return &Failure{"VALIDATION_FAILED", "Resource no longer available"}
	case "TEST_FAILURE":
		return &Failure{"TOOL_ERROR", "Capture unavailable"}
	case "INFRASTRUCTURE_FAILURE":
		return &Failure{"INFRASTRUCTURE_ERROR", "Execution unavailable"}
	default:
		return nil
	}
}

func TestEveryTransitionPair(t *testing.T) {
	b, _ := contract.Files.ReadFile("snapshot/transitions.json")
	var table map[string][]string
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	for from := range table {
		for to := range table {
			t.Run(from+"/"+to, func(t *testing.T) {
				r := Run{State: from, Revision: 2, CreatedAt: now, UpdatedAt: now}
				next, err := r.Transition(2, Change{State: to, Failure: failureFor(to)}, now.Add(time.Second))
				if !contract.CanTransition(from, to) {
					if !errors.Is(err, ErrTransition) {
						t.Fatalf("expected rejected edge: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if next.State != to || next.Revision != 3 || next.CreatedAt != now || !next.UpdatedAt.After(now) {
					t.Fatal("invalid mutation")
				}
				if (next.FinishedAt != nil) != contract.Terminal(to) {
					t.Fatal("invalid finish time")
				}
			})
		}
	}
}

func TestFailureAndRevisionValidation(t *testing.T) {
	now := time.Now().UTC()
	r := Run{State: "VALIDATING", Revision: 2, CreatedAt: now, UpdatedAt: now}
	for _, change := range []Change{
		{State: "INVALID"},
		{State: "INVALID", Failure: &Failure{"TOOL_ERROR", "invalid"}},
		{State: "PROVISIONING", Failure: &Failure{"TOOL_ERROR", "invalid"}},
		{State: "INVALID", Failure: &Failure{"VALIDATION_FAILED", ""}},
		{State: "INVALID", Failure: &Failure{"VALIDATION_FAILED", strings.Repeat("x", 1001)}},
	} {
		if _, err := r.Transition(2, change, now); !errors.Is(err, ErrValidation) {
			t.Fatalf("accepted failure mismatch: %v", err)
		}
	}
	if _, err := r.Transition(1, Change{State: "PROVISIONING"}, now); !errors.Is(err, ErrRevision) {
		t.Fatal(err)
	}
	next, err := r.Transition(2, Change{State: "PROVISIONING"}, now.Add(-time.Hour))
	if err != nil || !next.UpdatedAt.Equal(now) {
		t.Fatal("clock rollback changed ordering")
	}
	r.Revision = 9007199254740991
	if _, err := r.Transition(r.Revision, Change{State: "PROVISIONING"}, now); !errors.Is(err, ErrTransition) {
		t.Fatal("revision overflow")
	}
	for _, code := range []int{-1, 256} {
		r.Revision = 2
		if _, err := r.Transition(2, Change{State: "PROVISIONING", ToolExitCode: &code}, now); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid exit code")
		}
	}
}

func TestTool99DoesNotBecomeVerdict(t *testing.T) {
	r := Run{State: "RUNNING", Revision: 5, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	code := 99
	for _, state := range []string{"COLLECTING", "ANALYZING", "REPORTING", "COMPLETED"} {
		var err error
		r, err = r.Transition(r.Revision, Change{State: state, ToolExitCode: &code}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	code = 0
	if r.State != "COMPLETED" || *r.ToolExitCode != 99 || r.Failure != nil {
		t.Fatal("exit code incorrectly used as verdict or aliased")
	}
}
