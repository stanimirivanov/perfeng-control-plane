package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

type rerunJobReviewFixture struct{ denied bool }

func (s *rerunJobReviewFixture) ResolveJob(context.Context, string, run.Run) (*batchv1.Job, error) {
	return nil, run.ErrForbidden
}
func (s *rerunJobReviewFixture) ResolveRerunTemplate(context.Context, runrerun.Acceptance, run.Run) (*batchv1.Job, error) {
	if s.denied {
		return nil, run.ErrForbidden
	}
	return rerunTemplate(), nil
}
func TestRerunJobResolverSealsBeforeValidationAndRecoversCleanupAfterReopen(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	review := &rerunJobReviewFixture{}
	guard, err := reconcile.NewRerunJobResolver(r, review)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := r.ClaimRuns(testContext, "rerun-worker", 16, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var owned run.Claim
	for _, claim := range claims {
		if claim.Run.ID == current.ID {
			owned = claim
		}
	}
	if !owned.Lease.Valid() {
		t.Fatal("child not claimed")
	}
	job, err := guard.ResolveClaimJob(testContext, owned)
	if err != nil || !reflect.DeepEqual(job, rerunTemplate()) {
		t.Fatal("validation did not seal", err)
	}
	current, err = r.Advance(testContext, accepted.ChildPrincipal, current.ID, current.Revision, run.Change{State: run.StateProvisioning})
	if err != nil {
		t.Fatal(err)
	}
	if job, err = guard.ResolveJob(testContext, accepted.ChildPrincipal, current); !errors.Is(err, run.ErrUnavailable) || job != nil {
		t.Fatal("new dispatch fence bypassed", err)
	}
	current, err = r.Cancel(testContext, accepted.ChildPrincipal, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	review.denied = true
	restarted := openTest(t, dsn)
	guard, err = reconcile.NewRerunJobResolver(restarted, review)
	if err != nil {
		t.Fatal(err)
	}
	if job, err = guard.ResolveJob(testContext, accepted.ChildPrincipal, current); err != nil || !reflect.DeepEqual(job, rerunTemplate()) {
		t.Fatal("cleanup reconstructed withdrawn registry", err)
	}
}

func TestRerunValidationSealRequiresCurrentLease(t *testing.T) {
	r, _, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	stale := run.Claim{Run: current, Lease: run.Lease{RunID: current.ID, Principal: accepted.ChildPrincipal, WorkerID: "stale-worker", Token: strings.Repeat("a", 32)}}
	if job, err := r.RetainClaimedRerunTemplate(testContext, stale, rerunTemplate()); !errors.Is(err, run.ErrLeaseLost) || job != nil {
		t.Fatal("stale worker sealed template", err)
	}
	claims, err := r.ClaimRuns(testContext, "lease-guard", 16, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var owned run.Claim
	for _, claim := range claims {
		if claim.Run.ID == current.ID {
			owned = claim
		}
	}
	if !owned.Lease.Valid() {
		t.Fatal("child not claimed")
	}
	wrong := owned
	wrong.Lease.Token = strings.Repeat("f", 32)
	if job, err := r.RetainClaimedRerunTemplate(testContext, wrong, rerunTemplate()); !errors.Is(err, run.ErrLeaseLost) || job != nil {
		t.Fatal("wrong token sealed", err)
	}
	wrong = owned
	wrong.Run.Revision++
	if job, err := r.RetainClaimedRerunTemplate(testContext, wrong, rerunTemplate()); !errors.Is(err, run.ErrConflict) || job != nil {
		t.Fatal("stale revision sealed", err)
	}
	if err = r.ReleaseClaim(testContext, owned.Lease, 0); err != nil {
		t.Fatal(err)
	}
	if job, err := r.RetainClaimedRerunTemplate(testContext, owned, rerunTemplate()); !errors.Is(err, run.ErrLeaseLost) || job != nil {
		t.Fatal("expired lease sealed", err)
	}
	if job, err := r.GetRerunTemplate(testContext, accepted.ChildPrincipal, current); !errors.Is(err, run.ErrNotFound) || job != nil {
		t.Fatal("stale capture persisted", err)
	}
}
