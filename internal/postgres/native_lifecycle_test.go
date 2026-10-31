package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func v2RenewalRequest(
	fixture nativeV2LeaseFixture,
	revision int64,
	phase nativeworker.Phase,
) nativeworker.RenewalRequest {
	return nativeworker.RenewalRequest{
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: revision, Phase: phase,
		Capability: fixture.authority.capability,
	}
}

func v2ReleaseRequest(
	fixture nativeV2LeaseFixture,
	revision int64,
	reason nativeworker.ReleaseReason,
) nativeworker.ReleaseRequest {
	return nativeworker.ReleaseRequest{
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: revision, Reason: reason,
		Capability: fixture.authority.capability,
	}
}

func TestNativeV2RenewalHonorsRunPhasesAndCancellation(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	request := v2RenewalRequest(fixture, fixture.accepted.Run.Revision, nativeworker.PhasePreparing)
	preparing, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || !preparing.Valid() || preparing.CancellationRequested {
		t.Fatalf("v2 preparation renewal = %#v, err=%v", preparing, err)
	}
	current, err := repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current.State != run.StateWarmingUp || current.Revision != preparing.RunRevision {
		t.Fatalf("preparing Run = %#v, err=%v", current, err)
	}
	if _, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, request, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale v2 renewal = %v", err)
	}
	request = v2RenewalRequest(fixture, preparing.RunRevision, nativeworker.PhaseRunning)
	running, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || running.RunRevision <= preparing.RunRevision {
		t.Fatalf("v2 running renewal = %#v, err=%v", running, err)
	}
	request = v2RenewalRequest(fixture, running.RunRevision, nativeworker.PhasePreparing)
	if _, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, request, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("regressed v2 phase = %v", err)
	}
	cancelling, err := repository.Cancel(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || cancelling.State != run.StateCancelling {
		t.Fatalf("cancelling Run = %#v, err=%v", cancelling, err)
	}
	request = v2RenewalRequest(fixture, cancelling.Revision, nativeworker.PhaseUploading)
	decision, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || !decision.Valid() || !decision.CancellationRequested ||
		decision.RunRevision != cancelling.Revision {
		t.Fatalf("v2 cancellation decision = %#v, err=%v", decision, err)
	}
	current, err = repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current.State != run.StateCancelling || current.Revision != cancelling.Revision {
		t.Fatalf("cancellation was overwritten by phase: %#v, err=%v", current, err)
	}
	release := v2ReleaseRequest(
		fixture, cancelling.Revision, nativeworker.ReleaseCancellationRequested,
	)
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); err != nil {
		t.Fatal(err)
	}
	current, err = repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current.State != run.StateAborted || current.Revision != cancelling.Revision+1 {
		t.Fatalf("released v2 Run = %#v, err=%v", current, err)
	}
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); err != nil {
		t.Fatalf("exact v2 release retry failed: %v", err)
	}
	release.Reason = nativeworker.ReleaseWorkerShutdown
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("conflicting v2 release = %v", err)
	}
}

func TestNativeV2LeaseMutationsRejectWrongAuthorityAndProtocol(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	renew := v2RenewalRequest(fixture, fixture.accepted.Run.Revision, nativeworker.PhasePreparing)
	release := v2ReleaseRequest(fixture, fixture.accepted.Run.Revision, nativeworker.ReleaseWorkerShutdown)
	for _, test := range []struct {
		name     string
		workerID string
		change   func(*nativeworker.RenewalRequest, *nativeworker.ReleaseRequest)
	}{
		{"worker", "linux-workstation-02", func(*nativeworker.RenewalRequest, *nativeworker.ReleaseRequest) {}},
		{"attempt", fixture.workerID, func(r *nativeworker.RenewalRequest, s *nativeworker.ReleaseRequest) {
			r.AttemptID = "33333333-3333-4333-8333-333333333333"
			s.AttemptID = r.AttemptID
		}},
		{"capability", fixture.workerID, func(r *nativeworker.RenewalRequest, s *nativeworker.ReleaseRequest) {
			r.Capability = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			s.Capability = r.Capability
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			badRenew, badRelease := renew, release
			test.change(&badRenew, &badRelease)
			if _, err := repository.RenewNativeBaselineLease(
				testContext, test.workerID, badRenew, 2*time.Minute, 30*time.Second,
			); !errors.Is(err, nativeworker.ErrLeaseGone) {
				t.Fatalf("v2 renewal accepted wrong authority: %v", err)
			}
			if err := repository.ReleaseNativeBaselineLease(
				testContext, test.workerID, badRelease,
			); !errors.Is(err, nativeworker.ErrLeaseGone) {
				t.Fatalf("v2 release accepted wrong authority: %v", err)
			}
		})
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1
	`, fixture.accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID, renew, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired v2 lease renewed: %v", err)
	}
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired v2 lease released: %v", err)
	}
}

func TestNativeV2WorkerShutdownReleaseIsRetrySafe(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	release := v2ReleaseRequest(
		fixture, fixture.accepted.Run.Revision, nativeworker.ReleaseWorkerShutdown,
	)
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReleaseNativeBaselineLease(testContext, fixture.workerID, release); err != nil {
		t.Fatalf("identical v2 release retry failed: %v", err)
	}
	current, err := repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current != fixture.accepted.Run {
		t.Fatalf("coordination-only v2 release changed Run: %#v, err=%v", current, err)
	}
	var released bool
	var version int
	if err := repository.db.QueryRow(`
		SELECT released_at IS NOT NULL,protocol_version
		FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, fixture.accepted.Run.ID).Scan(&released, &version); err != nil ||
		!released || version != nativeLeaseProtocolV2 {
		t.Fatalf("v2 release marker = (%t, %d), err=%v", released, version, err)
	}
}
