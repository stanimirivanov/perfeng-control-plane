package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func rerunProvisioningFixture(t *testing.T) (*ProvisioningReconciler, *provisioningStore, *provisioningJobs, *rerunJobsFixture) {
	t.Helper()
	frozen, f := frozenRerunReport(t)
	f.current.State = run.StateProvisioning
	evidence := &rerunJobsFixture{rerunReportFixture: frozen, sealed: provisioningTemplate()}
	guard, err := NewRerunJobResolver(evidence, evidence)
	if err != nil {
		t.Fatal(err)
	}
	stage, store, jobs := provisioningFixture(t)
	store.claim = testRerunClaim(f.current)
	stage.resolver = guard
	return stage, store, jobs, evidence
}

func TestRerunProvisioningAdoptsAmbiguousCreateAfterResolverRestart(t *testing.T) {
	stage, store, jobs, evidence := rerunProvisioningFixture(t)
	jobs.failAfterCreate = true
	if _, err := stage.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) || jobs.job == nil || store.found {
		t.Fatal("ambiguous create lost its recoverable identity", err)
	}
	uid := jobs.job.UID
	restarted, err := NewRerunJobResolver(evidence, evidence)
	if err != nil {
		t.Fatal(err)
	}
	stage.resolver = restarted
	if _, err = stage.Reconcile(context.Background(), store.claim); err != nil || !store.found || store.execution.UID != uid || evidence.ordinaryCalls != 0 {
		t.Fatal("retry substituted selection or execution", err)
	}
	creates := jobs.creates
	if _, err = stage.Reconcile(context.Background(), store.claim); err != nil || jobs.creates != creates {
		t.Fatal("bound execution recreated", err)
	}
}

func TestRerunProvisioningRefusesUnqualifiedSelectionBeforeCreate(t *testing.T) {
	for _, mode := range []string{"withdrawn-registry", "withdrawn-eligibility", "missing-seal", "changed-seal", "lost-lease", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			stage, store, jobs, evidence := rerunProvisioningFixture(t)
			claim := store.claim
			expected := run.ErrForbidden
			switch mode {
			case "withdrawn-registry":
				evidence.reviewErr = run.ErrForbidden
			case "withdrawn-eligibility":
				evidence.sealErr = run.ErrForbidden
			case "missing-seal":
				evidence.sealed = nil
				expected = run.ErrConflict
			case "changed-seal":
				evidence.sealed.Spec.Template.Spec.Containers[0].Args = []string{"previous"}
				expected = run.ErrConflict
			case "lost-lease":
				store.renewErr = run.ErrConflict
				expected = run.ErrConflict
			case "cancelled":
				store.claim.Run.State = run.StateCancelling
				expected = nil
			}
			_, err := stage.Reconcile(context.Background(), claim)
			if !errors.Is(err, expected) || jobs.creates != 0 || store.found || evidence.ordinaryCalls != 0 {
				t.Fatal("unqualified selection reached dispatch", err)
			}
		})
	}
}

func TestRerunCancellationRecoversAmbiguousCreateAfterWithdrawal(t *testing.T) {
	stage, store, jobs, evidence := rerunProvisioningFixture(t)
	jobs.failAfterCreate = true
	if _, err := stage.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) || jobs.job == nil {
		t.Fatal("missing ambiguous Job", err)
	}
	evidence.reviewErr = run.ErrForbidden
	store.claim.Run.State = run.StateCancelling
	reviews := evidence.reviewCalls
	if _, err := stage.Reconcile(context.Background(), store.claim); err != nil || !store.found || jobs.creates != 1 || evidence.reviewCalls != reviews {
		t.Fatal("cleanup required current approval or created a Job", err)
	}
}
