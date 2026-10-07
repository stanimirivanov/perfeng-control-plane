package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func v2UploadRequest(fixture nativeV2LeaseFixture, slot string) nativeworker.UploadRequest {
	return nativeworker.UploadRequest{
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: fixture.accepted.Run.Revision,
		Slot:                slot, Size: 4096, SHA256: strings.Repeat("f", 64),
		MediaType: "application/json", Capability: fixture.authority.capability,
	}
}

func v2ArtifactRequest(
	request nativeworker.UploadRequest,
	reservation nativeworker.UploadReservation,
) nativeworker.ArtifactRegistrationRequest {
	return nativeworker.ArtifactRegistrationRequest{
		LeaseID: request.LeaseID, AttemptID: request.AttemptID,
		ExpectedRunRevision: request.ExpectedRunRevision, GrantID: reservation.GrantID,
		Slot: request.Slot, Size: request.Size, SHA256: request.SHA256,
		Capability: request.Capability,
	}
}

func TestNativeV2ArtifactsUseReviewedPlanAndRetainExactReferences(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	for _, planned := range fixture.plan.ArtifactPlan {
		t.Run(planned.Slot, func(t *testing.T) {
			request := v2UploadRequest(fixture, planned.Slot)
			reservation, err := repository.ReserveNativeBaselineUploadGrant(
				testContext, fixture.workerID, request, 2*time.Minute,
			)
			if err != nil || !reservation.Valid() || reservation.MaximumBytes != planned.MaximumBytes ||
				reservation.ExpiresAt.After(fixture.authority.expiresAt) {
				t.Fatalf("v2 reservation = %#v, err=%v", reservation, err)
			}
			retry, err := repository.ReserveNativeBaselineUploadGrant(
				testContext, fixture.workerID, request, 2*time.Minute,
			)
			if err != nil || retry.GrantID != reservation.GrantID {
				t.Fatalf("v2 grant retry = %#v, err=%v", retry, err)
			}
			registration := v2ArtifactRequest(request, reservation)
			candidate, err := repository.PrepareNativeBaselineArtifact(
				testContext, fixture.workerID, registration,
			)
			if err != nil || !candidate.Valid() || candidate.Kind != planned.Kind ||
				candidate.Format != planned.Format || candidate.Reservation != retry {
				t.Fatalf("v2 candidate = %#v, err=%v", candidate, err)
			}
			verified := nativeworker.VerifiedArtifact{
				Candidate: candidate, URI: "s3://perfeng-artifacts/" + reservation.ObjectKey,
			}
			first, err := repository.RegisterNativeBaselineArtifact(
				testContext, fixture.workerID, registration, verified,
			)
			if err != nil || !first.Valid() || first.Format != planned.Format ||
				first.URI != verified.URI {
				t.Fatalf("v2 artifact = %#v, err=%v", first, err)
			}
			second, err := repository.RegisterNativeBaselineArtifact(
				testContext, fixture.workerID, registration, verified,
			)
			if err != nil || second != first {
				t.Fatalf("v2 artifact retry = %#v, err=%v", second, err)
			}
		})
	}
}

func TestNativeV2ArtifactMutationsRejectChangedAndLostAuthority(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	request := v2UploadRequest(fixture, "measurements")
	for _, change := range []struct {
		name   string
		mutate func(*nativeworker.UploadRequest)
		want   error
	}{
		{"unplanned slot", func(r *nativeworker.UploadRequest) { r.Slot = "trace" }, nativeworker.ErrArtifactPlan},
		{"media type", func(r *nativeworker.UploadRequest) { r.MediaType = "application/octet-stream" }, nativeworker.ErrArtifactPlan},
		{"oversized", func(r *nativeworker.UploadRequest) { r.Size = fixture.plan.ArtifactPlan[0].MaximumBytes + 1 }, nativeworker.ErrArtifactPlan},
		{"stale revision", func(r *nativeworker.UploadRequest) { r.ExpectedRunRevision++ }, run.ErrRevision},
		{"wrong capability", func(r *nativeworker.UploadRequest) { r.Capability = strings.Repeat("e", 64) }, nativeworker.ErrLeaseGone},
	} {
		t.Run(change.name, func(t *testing.T) {
			bad := request
			change.mutate(&bad)
			if _, err := repository.ReserveNativeBaselineUploadGrant(
				testContext, fixture.workerID, bad, 2*time.Minute,
			); !errors.Is(err, change.want) {
				t.Fatalf("v2 grant rejection = %v, want %v", err, change.want)
			}
		})
	}
	reservation, err := repository.ReserveNativeBaselineUploadGrant(
		testContext, fixture.workerID, request, 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.SHA256 = strings.Repeat("d", 64)
	if _, err := repository.ReserveNativeBaselineUploadGrant(
		testContext, fixture.workerID, changed, 2*time.Minute,
	); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("changed v2 declaration = %v", err)
	}
	registration := v2ArtifactRequest(request, reservation)
	changedRegistration := registration
	changedRegistration.Size++
	if _, err := repository.PrepareNativeBaselineArtifact(
		testContext, fixture.workerID, changedRegistration,
	); !errors.Is(err, nativeworker.ErrUploadGrantMismatch) {
		t.Fatalf("changed v2 registration = %v", err)
	}
	candidate, err := repository.PrepareNativeBaselineArtifact(
		testContext, fixture.workerID, registration,
	)
	if err != nil {
		t.Fatal(err)
	}
	verified := nativeworker.VerifiedArtifact{
		Candidate: candidate, URI: "s3://perfeng-artifacts/" + reservation.ObjectKey,
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1
	`, fixture.accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RegisterNativeBaselineArtifact(
		testContext, fixture.workerID, registration, verified,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired v2 authority registered artifact: %v", err)
	}
}
