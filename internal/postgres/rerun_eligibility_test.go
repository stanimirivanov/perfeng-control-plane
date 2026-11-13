package postgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

func validatingRerunClaim(t *testing.T, r *Repository, accepted runrerun.Acceptance) run.Claim {
	t.Helper()
	if _, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating}); err != nil {
		t.Fatal(err)
	}
	claims, err := r.ClaimRuns(testContext, "eligibility-worker", 16, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.Run.ID == accepted.Child.ID {
			return claim
		}
	}
	t.Fatal("child not claimed")
	return run.Claim{}
}

func TestRerunValidationEligibilityRejectsWithdrawnAuthorityAndApproval(t *testing.T) {
	for _, mode := range []string{"read", "rerun", "baseline"} {
		for _, sealed := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/first", true: "/replay"}[sealed], func(t *testing.T) {
				r, _, intent := rerunFixture(t)
				accepted, err := r.AcceptRerun(testContext, intent)
				if err != nil {
					t.Fatal(err)
				}
				claim := validatingRerunClaim(t, r, accepted)
				if sealed {
					if _, err = r.RetainClaimedRerunTemplate(testContext, claim, rerunTemplate()); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "baseline" {
					record := accepted.Inputs.BaselineSnapshots[0]
					if _, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "withdrawn before validation"}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err = serviceGrant(t, r.db, "search", "maya", "run:"+mode, false); err != nil {
						t.Fatal(err)
					}
				}
				if job, err := r.RetainClaimedRerunTemplate(testContext, claim, rerunTemplate()); !errors.Is(err, run.ErrForbidden) || job != nil {
					t.Fatal("withdrawal bypassed validation", err)
				}
				job, err := r.GetRerunTemplate(testContext, accepted.ChildPrincipal, claim.Run)
				if sealed {
					if err != nil || !reflect.DeepEqual(job, rerunTemplate()) {
						t.Fatal("historical seal destroyed", err)
					}
				} else if !errors.Is(err, run.ErrNotFound) || job != nil {
					t.Fatal("failed validation persisted a seal", err)
				}
			})
		}
	}
}
