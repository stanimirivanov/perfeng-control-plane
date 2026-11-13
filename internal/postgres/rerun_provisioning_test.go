package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestRerunProvisioningReplayRequiresCurrentEligibilityAndExistingSeal(t *testing.T) {
	for _, mode := range []string{"approved", "read", "rerun", "baseline", "missing-seal", "stale-token", "stale-revision"} {
		t.Run(mode, func(t *testing.T) {
			r, dsn, intent := rerunFixture(t)
			accepted, err := r.AcceptRerun(testContext, intent)
			if err != nil {
				t.Fatal(err)
			}
			claim := validatingRerunClaim(t, r, accepted)
			if mode != "missing-seal" {
				if _, err = r.RetainClaimedRerunTemplate(testContext, claim, rerunTemplate()); err != nil {
					t.Fatal(err)
				}
			}
			claim.Run, err = r.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: run.StateProvisioning})
			if err != nil {
				t.Fatal(err)
			}
			var expected error
			switch mode {
			case "read", "rerun":
				if err = serviceGrant(t, r.db, "search", "maya", "run:"+mode, false); err != nil {
					t.Fatal(err)
				}
				expected = run.ErrForbidden
			case "baseline":
				record := accepted.Inputs.BaselineSnapshots[0]
				if _, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "withdrawn before dispatch"}); err != nil {
					t.Fatal(err)
				}
				expected = run.ErrForbidden
			case "missing-seal":
				expected = run.ErrConflict
			case "stale-token":
				claim.Lease.Token = strings.Repeat("f", 32)
				expected = run.ErrLeaseLost
			case "stale-revision":
				claim.Run.Revision--
				expected = run.ErrConflict
			}
			restarted := openTest(t, dsn)
			guard, err := reconcile.NewRerunJobResolver(restarted, &rerunJobReviewFixture{})
			if err != nil {
				t.Fatal(err)
			}
			job, err := guard.ResolveClaimJob(testContext, claim)
			if !errors.Is(err, expected) || (expected != nil && job != nil) || (expected == nil && !reflect.DeepEqual(job, rerunTemplate())) {
				t.Fatal("provisioning bypassed claim/eligibility or changed template", err)
			}
			historical, err := r.GetRerunTemplate(testContext, accepted.ChildPrincipal, claim.Run)
			if mode == "missing-seal" {
				if !errors.Is(err, run.ErrNotFound) || historical != nil {
					t.Fatal("provisioning fabricated validation history", err)
				}
			} else if err != nil || !reflect.DeepEqual(historical, rerunTemplate()) {
				t.Fatal("eligibility failure damaged historical seal", err)
			}
		})
	}
}
