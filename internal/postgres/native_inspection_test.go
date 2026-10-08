package postgres

import (
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
)

func TestNativeAttemptInspectionIsWorkerScopedAndReadOnly(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	request := nativeworker.InspectionRequest{
		RunID: fixture.accepted.Run.ID, AttemptID: fixture.authority.attemptID,
	}
	active, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID, request,
	)
	if err != nil || !active.Valid() || active.State != nativeworker.InspectionActive ||
		active.LeaseID != fixture.authority.leaseID ||
		active.RunRevision != fixture.accepted.Run.Revision ||
		!active.ExpiresAt.Equal(fixture.authority.expiresAt) {
		t.Fatalf("active inspection = %#v, err=%v", active, err)
	}
	for _, workerID := range []string{"another-windows-worker", fixture.workerID} {
		lookup := request
		if workerID == fixture.workerID {
			lookup.AttemptID = "33333333-3333-4333-8333-333333333333"
		}
		if _, err := fixture.repository.InspectNativeBaselineAttempt(
			testContext, workerID, lookup,
		); !errors.Is(err, nativeworker.ErrAttemptNotFound) {
			t.Fatalf("worker=%s, lookup=%#v: %v", workerID, lookup, err)
		}
	}
	again, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID, request,
	)
	if err != nil || !again.Valid() || again.State != nativeworker.InspectionActive ||
		again.RunRevision != active.RunRevision || again.LeaseID != active.LeaseID ||
		!again.ExpiresAt.Equal(*active.ExpiresAt) {
		t.Fatalf("inspection changed lease authority: %#v, err=%v", again, err)
	}
	if err := fixture.repository.ReleaseNativeBaselineLease(
		testContext, fixture.workerID,
		v2ReleaseRequest(fixture, fixture.accepted.Run.Revision, nativeworker.ReleaseWorkerShutdown),
	); err != nil {
		t.Fatal(err)
	}
	inactive, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID, request,
	)
	if err != nil || !inactive.Valid() || inactive.State != nativeworker.InspectionInactive ||
		inactive.LeaseID != "" || inactive.ExpiresAt != nil || inactive.RunRevision != 0 {
		t.Fatalf("inactive inspection = %#v, err=%v", inactive, err)
	}
}

func TestNativeAttemptInspectionPrefersAcceptedTerminalReport(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	request := nativeworker.TerminalRequest{
		Kind:    nativeworker.TerminalFailed,
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: fixture.accepted.Run.Revision,
		Failure: &nativeworker.TerminalFailure{
			Code:    nativeworker.TerminalFailurePreparation,
			Message: "preparation failed", Retryable: false,
		},
		Capability: fixture.authority.capability,
	}
	ack, err := fixture.repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID,
		nativeworker.InspectionRequest{
			RunID: fixture.accepted.Run.ID, AttemptID: fixture.authority.attemptID,
		},
	)
	if err != nil || !inspection.Valid() || inspection.State != nativeworker.InspectionTerminal ||
		inspection.TerminalKind != nativeworker.TerminalFailed ||
		inspection.RunRevision != ack.RunRevision || inspection.AcceptedAt == nil ||
		inspection.LeaseID != "" || inspection.ExpiresAt != nil {
		t.Fatalf("terminal inspection = %#v, err=%v", inspection, err)
	}
}

func TestNativeAttemptInspectionExpiresAndLosesSupersededRows(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	request := nativeworker.InspectionRequest{
		RunID: fixture.accepted.Run.ID, AttemptID: fixture.authority.attemptID,
	}
	if _, err := fixture.repository.db.ExecContext(testContext, `
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '2 days',
			expires_at=clock_timestamp()-interval '1 day'
		WHERE run_id=$1
	`, request.RunID); err != nil {
		t.Fatal(err)
	}
	inactive, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID, request,
	)
	if err != nil || !inactive.Valid() || inactive.State != nativeworker.InspectionInactive {
		t.Fatalf("expired inspection = %#v, err=%v", inactive, err)
	}
	if _, err := fixture.repository.db.ExecContext(testContext, `
		UPDATE perfeng_control.native_execution_leases
		SET lease_id='44444444-4444-4444-8444-444444444444',
			attempt_id='55555555-5555-4555-8555-555555555555'
		WHERE run_id=$1
	`, request.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.InspectNativeBaselineAttempt(
		testContext, fixture.workerID, request,
	); !errors.Is(err, nativeworker.ErrAttemptNotFound) {
		t.Fatalf("superseded attempt remained observable: %v", err)
	}
}
