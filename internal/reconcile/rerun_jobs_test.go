package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

type rerunJobsFixture struct {
	*rerunReportFixture
	sealed                     *batchv1.Job
	reviewErr, sealErr         error
	reviewCalls, ordinaryCalls int
}

func (s *rerunJobsFixture) GetRerunTemplate(context.Context, string, run.Run) (*batchv1.Job, error) {
	if s.sealErr != nil {
		return nil, s.sealErr
	}
	if s.sealed == nil {
		return nil, run.ErrNotFound
	}
	return s.sealed.DeepCopy(), nil
}
func (s *rerunJobsFixture) RetainRerunTemplate(_ context.Context, _ string, _ run.Run, job *batchv1.Job) (*batchv1.Job, error) {
	if s.sealErr != nil {
		return nil, s.sealErr
	}
	if s.sealed != nil && !reflect.DeepEqual(s.sealed, job) {
		return nil, run.ErrConflict
	}
	s.sealed = job.DeepCopy()
	return s.sealed.DeepCopy(), nil
}
func (s *rerunJobsFixture) ResolveRerunTemplate(context.Context, runrerun.Acceptance, run.Run) (*batchv1.Job, error) {
	s.reviewCalls++
	if s.reviewErr != nil {
		return nil, s.reviewErr
	}
	return provisioningTemplate(), nil
}
func (s *rerunJobsFixture) ResolveJob(context.Context, string, run.Run) (*batchv1.Job, error) {
	s.ordinaryCalls++
	return provisioningTemplate(), nil
}

func TestRerunJobsSealValidationAndRecoverCleanupWithoutRegistry(t *testing.T) {
	frozen, f := frozenRerunReport(t)
	store := &rerunJobsFixture{rerunReportFixture: frozen}
	resolver, err := NewRerunJobResolver(store, store)
	if err != nil {
		t.Fatal(err)
	}
	f.current.State = run.StateValidating
	job, err := resolver.ResolveClaimJob(context.Background(), testRerunClaim(f.current))
	if err != nil || store.sealed == nil || store.ordinaryCalls != 0 {
		t.Fatal("validation bypassed seal", err)
	}
	job.Spec.Template.Spec.Containers[0].Args = []string{"mutated"}
	again, err := resolver.ResolveClaimJob(context.Background(), testRerunClaim(f.current))
	if err != nil || !reflect.DeepEqual(again, store.sealed) {
		t.Fatal("retry drift", err)
	}
	store.reviewErr = run.ErrForbidden
	f.current.State = run.StateCancelling
	calls := store.reviewCalls
	if _, err = resolver.ResolveClaimJob(context.Background(), testRerunClaim(f.current)); err != nil || store.reviewCalls != calls {
		t.Fatal("cleanup depends on withdrawn registry", err)
	}
	f.current.State = run.StateProvisioning
	if job, err = resolver.ResolveClaimJob(context.Background(), testRerunClaim(f.current)); !errors.Is(err, run.ErrUnavailable) || job != nil {
		t.Fatal("unqualified dispatch enabled", err)
	}
}
func TestRerunJobsDoNotFallbackOnBrokenKnownSelection(t *testing.T) {
	for _, mode := range []string{"missing", "review-denied", "seal-failed", "missing-cleanup", "changed-seal"} {
		t.Run(mode, func(t *testing.T) {
			frozen, f := frozenRerunReport(t)
			store := &rerunJobsFixture{rerunReportFixture: frozen}
			f.current.State = run.StateValidating
			switch mode {
			case "missing":
				store.lookupErr = run.ErrNotFound
			case "review-denied":
				store.reviewErr = run.ErrForbidden
			case "seal-failed":
				store.sealErr = run.ErrUnavailable
			case "missing-cleanup":
				f.current.State = run.StateCancelling
			case "changed-seal":
				store.sealed = provisioningTemplate()
				store.sealed.Spec.Template.Spec.Containers[0].Args = []string{"previous"}
			}
			resolver, _ := NewRerunJobResolver(store, store)
			job, err := resolver.ResolveClaimJob(context.Background(), testRerunClaim(f.current))
			if err == nil || job != nil || store.ordinaryCalls != 0 {
				t.Fatal("known rerun bypassed guard", err)
			}
		})
	}
	frozen, f := frozenRerunReport(t)
	frozen.known = false
	store := &rerunJobsFixture{rerunReportFixture: frozen}
	resolver, _ := NewRerunJobResolver(store, store)
	if _, err := resolver.ResolveJob(context.Background(), "ordinary", f.current); err != nil || store.ordinaryCalls != 1 {
		t.Fatal("ordinary resolution changed", err)
	}
}

func (s *rerunJobsFixture) RetainClaimedRerunTemplate(ctx context.Context, claim run.Claim, job *batchv1.Job) (*batchv1.Job, error) {
	return s.RetainRerunTemplate(ctx, claim.Lease.Principal, claim.Run, job)
}
func testRerunClaim(current run.Run) run.Claim {
	return run.Claim{Run: current, Lease: run.Lease{RunID: current.ID, Principal: "child-owner", WorkerID: "worker", Token: strings.Repeat("a", 32)}}
}

func TestRerunValidationReconcilerCarriesClaimIntoSealing(t *testing.T) {
	frozen, f := frozenRerunReport(t)
	store := &rerunJobsFixture{rerunReportFixture: frozen}
	guard, err := NewRerunJobResolver(store, store)
	if err != nil {
		t.Fatal(err)
	}
	stage, advanced, jobs := validationFixture(t)
	stage.resolver = guard
	f.current.State = run.StateValidating
	claim := testRerunClaim(f.current)
	if _, err = stage.Reconcile(context.Background(), claim); err != nil || advanced.change.State != run.StateProvisioning || store.sealed == nil {
		t.Fatal("validation bypassed claim-aware seal", err)
	}
	if jobs.creates != 0 || len(jobs.store.events) != 0 {
		t.Fatal("validation contacted Kubernetes")
	}
	if job, err := guard.ResolveJob(context.Background(), "child-owner", f.current); !errors.Is(err, run.ErrValidation) || job != nil {
		t.Fatal("unclaimed validation sealed", err)
	}
}
