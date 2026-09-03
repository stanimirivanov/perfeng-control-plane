package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type advancingStore struct {
	change   run.Change
	lease    run.Lease
	revision int64
	err      error
	calls    int
}

func (store *advancingStore) AdvanceClaim(
	ctx context.Context,
	lease run.Lease,
	revision int64,
	change run.Change,
) (run.Run, error) {
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	store.calls++
	store.lease, store.revision, store.change = lease, revision, change

	return run.Run{}, store.err
}

func validationFixture(t *testing.T) (*ValidationReconciler, *advancingStore, *provisioningJobs) {
	t.Helper()

	store := &advancingStore{}
	jobStore := &provisioningStore{claim: boundClaim(run.StateValidating)}
	jobs := &provisioningJobs{store: jobStore}
	dispatcher, err := kubernetes.NewDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	resolver := resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
		return provisioningTemplate(), nil
	})
	reconciler, err := NewValidationReconciler(store, resolver, dispatcher, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return reconciler, store, jobs
}

func TestValidationAdvancesApprovedPlanWithoutCreatingJob(t *testing.T) {
	reconciler, store, jobs := validationFixture(t)
	claim := boundClaim(run.StateValidating)

	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	if store.calls != 1 || store.change.State != run.StateProvisioning ||
		store.change.Failure != nil || store.lease != claim.Lease ||
		store.revision != claim.Run.Revision || jobs.creates != 0 || jobs.job != nil {
		t.Fatalf("unexpected validation effects: %+v, creates=%d", store, jobs.creates)
	}
}

func TestValidationRejectsUnsafePlansWithSafeFailure(t *testing.T) {
	tests := []struct {
		name     string
		resolved *batchv1.Job
		err      error
		message  string
	}{
		{"resolver validation", nil, errors.Join(run.ErrValidation, errors.New("token=secret")), "approved execution plan is invalid"},
		{"nil plan", nil, nil, "approved execution plan is invalid"},
		{"invalid job", &batchv1.Job{}, nil, "approved execution plan is invalid"},
		{"revoked", nil, run.ErrForbidden, "execution resources are not authorized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reconciler, store, jobs := validationFixture(t)
			reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
				return test.resolved, test.err
			})

			if _, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateValidating)); err != nil {
				t.Fatal(err)
			}
			failure := store.change.Failure
			if store.calls != 1 || store.change.State != run.StateInvalid || failure == nil ||
				failure.Code != run.FailureCodeValidationFailed || failure.Message != test.message ||
				strings.Contains(failure.Message, "secret") || jobs.creates != 0 {
				t.Fatalf("unsafe rejection: %+v", store.change)
			}
		})
	}
}

func TestValidationPreservesRetryableAndOwnershipErrors(t *testing.T) {
	for _, want := range []error{run.ErrUnavailable, context.Canceled, run.ErrLeaseLost} {
		t.Run(want.Error(), func(t *testing.T) {
			reconciler, store, _ := validationFixture(t)
			reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
				return nil, want
			})
			if _, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateValidating)); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if store.calls != 0 {
				t.Fatal("retryable resolution error changed lifecycle")
			}
		})
	}
}

func TestValidationRediscoverAfterRevisionRace(t *testing.T) {
	reconciler, store, _ := validationFixture(t)
	store.err = run.ErrRevision
	if result, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateValidating)); err != nil || result.RetryAfter != 0 {
		t.Fatalf("revision race = %+v, %v", result, err)
	}
	store.err = run.ErrLeaseLost
	if _, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateValidating)); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("lease loss error = %v", err)
	}
}

func TestValidationHonorsResolutionDeadline(t *testing.T) {
	reconciler, store, _ := validationFixture(t)
	reconciler.attemptTimeout = 5 * time.Millisecond
	reconciler.resolver = resolveJobFunc(func(ctx context.Context, _ string, _ run.Run) (*batchv1.Job, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	})
	if _, err := reconciler.Reconcile(context.Background(), boundClaim(run.StateValidating)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if store.calls != 0 {
		t.Fatal("timed-out resolution changed lifecycle")
	}
}

func TestValidationValidatesDependenciesClaimsAndState(t *testing.T) {
	reconciler, store, jobs := validationFixture(t)
	for _, timeout := range []time.Duration{0, -time.Second, 5*time.Minute + time.Nanosecond} {
		if _, err := NewValidationReconciler(store, reconciler.resolver, reconciler.validator, timeout); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("timeout %s error = %v", timeout, err)
		}
	}
	if _, err := NewValidationReconciler(nil, reconciler.resolver, reconciler.validator, time.Second); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewValidationReconciler(store, nil, reconciler.validator, time.Second); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewValidationReconciler(store, reconciler.resolver, nil, time.Second); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}

	claim := boundClaim(run.StateValidating)
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	claim = boundClaim(run.StateCreated)
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, ErrStateNotHandled) {
		t.Fatal(err)
	}
	if store.calls != 0 || jobs.creates != 0 {
		t.Fatal("invalid input accessed mutable dependencies")
	}
}
