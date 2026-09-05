package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func executionFor(runID string) kubernetes.Execution {
	return kubernetes.Execution{
		RunID:      runID,
		Namespace:  "perf-runs",
		JobName:    runID,
		UID:        types.UID("af92431e-e3b0-4e34-9dba-bc44f3c28ca9"),
		SpecSHA256: strings.Repeat("a", 64),
	}
}

func TestExecutionBindingLifecycle(t *testing.T) {
	repository, dsn := claimsDB(t)
	other := openTest(t, dsn)
	accepted(t, repository, "execution", "request-key-execution")
	claim := oneClaim(t, repository, "execution-worker")

	if execution, found, err := repository.GetExecution(testContext, claim.Lease); err != nil ||
		found || execution != (kubernetes.Execution{}) {
		t.Fatal("missing execution was not reported", execution, found, err)
	}
	execution := executionFor(claim.Run.ID)
	if err := repository.BindExecution(testContext, claim.Lease, execution); err != nil {
		t.Fatal(err)
	}
	if err := other.BindExecution(testContext, claim.Lease, execution); err != nil {
		t.Fatal("identical execution retry failed", err)
	}
	stored, found, err := other.GetExecution(testContext, claim.Lease)
	if err != nil || !found || stored != execution {
		t.Fatal("execution identity was not durable", stored, found, err)
	}

	changed := execution
	changed.UID = types.UID("8d149bec-7953-4218-a798-76900b202003")
	if err := repository.BindExecution(testContext, claim.Lease, changed); !errors.Is(err, kubernetes.ErrExecutionConflict) {
		t.Fatal("execution identity was overwritten", err)
	}
	stored, found, err = repository.GetExecution(testContext, claim.Lease)
	if err != nil || !found || stored != execution {
		t.Fatal("conflict changed the stored execution", stored, found, err)
	}

	forged := claim.Lease
	forged.Principal = "other"
	if _, _, err := repository.GetExecution(testContext, forged); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("forged lease read execution identity", err)
	}
	if err := repository.BindExecution(testContext, forged, execution); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("forged lease bound execution identity", err)
	}

	invalid := execution
	invalid.SpecSHA256 = "invalid"
	if err := repository.BindExecution(testContext, claim.Lease, invalid); !errors.Is(err, run.ErrValidation) {
		t.Fatal("invalid execution identity reached storage", err)
	}

	cancelled, cancel := context.WithCancel(testContext)
	cancel()
	if _, _, err := repository.GetExecution(cancelled, claim.Lease); !errors.Is(err, context.Canceled) {
		t.Fatal("execution read lost context cancellation", err)
	}
}

func TestExecutionCanBeBoundAfterCancellation(t *testing.T) {
	repository, _ := claimsDB(t)
	acceptedRun := accepted(t, repository, "cancelled-execution", "request-key-cancelled-execution")
	claim := oneClaim(t, repository, "cancelled-execution-worker")
	current, err := repository.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.AdvanceClaim(testContext, claim.Lease, current.Revision, run.Change{State: run.StateProvisioning}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Cancel(testContext, "cancelled-execution", acceptedRun.Run.ID); err != nil {
		t.Fatal(err)
	}
	execution := executionFor(claim.Run.ID)
	if err := repository.BindExecution(testContext, claim.Lease, execution); err != nil {
		t.Fatal("late execution identity was lost after cancellation", err)
	}
	stored, found, err := repository.GetExecution(testContext, claim.Lease)
	if err != nil || !found || stored != execution {
		t.Fatal("cancelled Run cannot recover its execution", stored, found, err)
	}
}

func TestConcurrentExecutionBindingKeepsOneIdentity(t *testing.T) {
	repository, dsn := claimsDB(t)
	other := openTest(t, dsn)
	accepted(t, repository, "concurrent-execution", "request-key-concurrent-execution")
	claim := oneClaim(t, repository, "concurrent-execution-worker")
	first := executionFor(claim.Run.ID)
	second := first
	second.UID = types.UID("8d149bec-7953-4218-a798-76900b202003")

	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for index, candidate := range []kubernetes.Execution{first, second} {
		store := repository
		if index == 1 {
			store = other
		}
		wait.Go(func() { errorsFound <- store.BindExecution(testContext, claim.Lease, candidate) })
	}
	wait.Wait()
	close(errorsFound)

	succeeded, conflicted := 0, 0
	for err := range errorsFound {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, kubernetes.ErrExecutionConflict):
			conflicted++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatal("concurrent bindings did not select exactly one identity")
	}
	stored, found, err := repository.GetExecution(testContext, claim.Lease)
	if err != nil || !found || (stored != first && stored != second) {
		t.Fatal("winning execution identity was not retained", stored, found, err)
	}
}
