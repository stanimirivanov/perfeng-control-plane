package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestRerunExecutionLookupUsesExactChildIdentity(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, dsn)
	got, err := restarted.GetRerunExecution(testContext, first.ChildPrincipal, first.Child)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("restart changed frozen execution", err)
	}
	got.Inputs.PolicyBytes[0] = 'x'
	got, err = restarted.GetRerunExecution(testContext, first.ChildPrincipal, first.Child)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("returned inputs alias retained evidence", err)
	}
	other := intent
	other.Key = "sibling-rerun-0001"
	sibling, err := r.AcceptRerun(testContext, other)
	if err != nil {
		t.Fatal(err)
	}
	substituted := first.Child
	substituted.Request.Candidate.GitSHA = "ffffffffffffffffffffffffffffffffffffffff"
	for _, selection := range []struct {
		owner   string
		current run.Run
	}{
		{"alice", first.Child}, {first.ChildPrincipal, run.Run{ID: intent.SourceRunID, Request: first.Child.Request}},
		{first.ChildPrincipal, substituted}, {"unrelated-owner", sibling.Child},
	} {
		value, err := r.GetRerunExecution(testContext, selection.owner, selection.current)
		if !errors.Is(err, run.ErrNotFound) || value.ID != "" {
			t.Fatal("unmatched child selection disclosed evidence", err)
		}
	}
	ordinary := accepted(t, r, "alice", "ordinary-no-rerun01").Run
	if value, err := r.GetRerunExecution(testContext, "alice", ordinary); !errors.Is(err, run.ErrNotFound) || value.ID != "" {
		t.Fatal("ordinary Run inferred into rerun", err)
	}
}

func TestRerunExecutionPreservesEvidenceWithoutGrantOrApprovalFallback(t *testing.T) {
	r, _, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{"run:read", "run:rerun"} {
		if err = serviceGrant(t, r.db, "search", "maya", permission, false); err != nil {
			t.Fatal(err)
		}
	}
	record := first.Inputs.BaselineSnapshots[0]
	if _, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "retired after acceptance"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("interactive recovery ignored revocation", err)
	}
	got, err := r.GetRerunExecution(testContext, first.ChildPrincipal, first.Child)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("worker evidence rewritten by current grants/approval", err)
	}
	if got.Inputs.BaselineSnapshots[0].State != baseline.StateApproved {
		t.Fatal("historical approval rewritten")
	}
}

func TestRerunExecutionCancellationDoesNotReturnEvidence(t *testing.T) {
	r, _, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	value, err := r.GetRerunExecution(ctx, first.ChildPrincipal, first.Child)
	if !errors.Is(err, context.Canceled) || value.ID != "" {
		t.Fatal("canceled lookup returned evidence", err)
	}
}

func TestRerunExecutionFailsClosedOnStoredRequestSubstitution(t *testing.T) {
	r, _, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.db.Exec(`UPDATE perfeng_control.runs SET snapshot=jsonb_set(snapshot,'{request,candidate,gitSha}',to_jsonb(repeat('f',40))) WHERE run_id=$1`, first.Child.ID)
	if err != nil {
		t.Fatal(err)
	}
	corrupted, err := r.Get(testContext, first.ChildPrincipal, first.Child.ID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := r.GetRerunExecution(testContext, first.ChildPrincipal, corrupted)
	if !errors.Is(err, run.ErrUnavailable) || value.ID != "" {
		t.Fatal("stored request substitution trusted", err)
	}
}

func TestRerunExecutionReadFailureDoesNotMasqueradeAsAbsence(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := openTest(t, dsn)
	if err = unavailable.Close(); err != nil {
		t.Fatal(err)
	}
	value, err := unavailable.GetRerunExecution(testContext, first.ChildPrincipal, first.Child)
	if !errors.Is(err, run.ErrUnavailable) || value.ID != "" {
		t.Fatal("storage outage presented as absence", err)
	}
}
