package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// PrepareNativeBaselineArtifact selects a v2 upload grant and its immutable
// reviewed artifact semantics before object verification.
func (r *Repository) PrepareNativeBaselineArtifact(
	ctx context.Context,
	workerID string,
	request nativeworker.ArtifactRegistrationRequest,
) (nativeworker.ArtifactCandidate, error) {
	return r.prepareNativeArtifact(ctx, workerID, request, nativeLeaseProtocolV2)
}

func (r *Repository) prepareNativeArtifact(
	ctx context.Context,
	workerID string,
	request nativeworker.ArtifactRegistrationRequest,
	protocolVersion int,
) (nativeworker.ArtifactCandidate, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return nativeworker.ArtifactCandidate{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.ArtifactCandidate{}, err
	}
	defer tx.Rollback()

	candidate, _, err := r.lockNativeArtifactCandidate(ctx, tx, workerID, request, protocolVersion)
	return candidate, err
}

// RegisterNativeBaselineArtifact rechecks v2 lease and grant authority after
// object verification, retaining the first accepted reference on exact retry.
func (r *Repository) RegisterNativeBaselineArtifact(
	ctx context.Context,
	workerID string,
	request nativeworker.ArtifactRegistrationRequest,
	verified nativeworker.VerifiedArtifact,
) (nativeworker.ArtifactReference, error) {
	return r.registerNativeArtifact(ctx, workerID, request, verified, nativeLeaseProtocolV2)
}

func (r *Repository) registerNativeArtifact(
	ctx context.Context,
	workerID string,
	request nativeworker.ArtifactRegistrationRequest,
	verified nativeworker.VerifiedArtifact,
	protocolVersion int,
) (nativeworker.ArtifactReference, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() || !verified.Valid() {
		return nativeworker.ArtifactReference{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.ArtifactReference{}, err
	}
	defer tx.Rollback()

	candidate, scope, err := r.lockNativeArtifactCandidate(ctx, tx, workerID, request, protocolVersion)
	if err != nil {
		return nativeworker.ArtifactReference{}, err
	}
	if verified.Candidate != candidate {
		return nativeworker.ArtifactReference{}, nativeworker.ErrUploadGrantMismatch
	}

	reference, found, err := lockedNativeArtifact(ctx, tx, request.GrantID)
	if err != nil {
		return nativeworker.ArtifactReference{}, err
	}
	if found {
		if !nativeArtifactMatches(reference, verified) {
			return nativeworker.ArtifactReference{}, nativeworker.ErrUploadGrantMismatch
		}
		if err = tx.Commit(); err != nil {
			return nativeworker.ArtifactReference{}, storageError(err)
		}

		return reference, nil
	}

	artifactID, err := newUUID()
	if err != nil {
		return nativeworker.ArtifactReference{}, run.ErrUnavailable
	}
	reference = nativeworker.ArtifactReference{
		ArtifactID:   artifactID,
		RunID:        candidate.Reservation.RunID,
		AttemptID:    candidate.Reservation.AttemptID,
		Slot:         candidate.Reservation.Slot,
		Kind:         candidate.Kind,
		URI:          verified.URI,
		Size:         candidate.Reservation.Size,
		SHA256:       candidate.Reservation.SHA256,
		MediaType:    candidate.Reservation.MediaType,
		RegisteredAt: scope.now,
		Format:       candidate.Format,
	}
	if !reference.Valid() {
		return nativeworker.ArtifactReference{}, errors.New("invalid native artifact reference")
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.native_artifacts(
			artifact_id,grant_id,uri,kind,format,registered_at
		) VALUES ($1,$2,$3,$4,$5,$6)
	`, reference.ArtifactID, request.GrantID, reference.URI, reference.Kind,
		reference.Format, reference.RegisteredAt); err != nil {
		return nativeworker.ArtifactReference{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return nativeworker.ArtifactReference{}, storageError(err)
	}

	return reference, nil
}

func (r *Repository) lockNativeArtifactCandidate(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	request nativeworker.ArtifactRegistrationRequest,
	protocolVersion int,
) (nativeworker.ArtifactCandidate, nativeRenewalScope, error) {
	scope, err := r.lockNativeRenewalScopeVersion(ctx, tx, workerID, request.LeaseID, protocolVersion)
	if err != nil {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{}, err
	}
	if !matchingNativeLeaseAuthority(
		scope.stored,
		workerID,
		request.LeaseID,
		request.AttemptID,
		request.Capability,
	) || scope.stored.releasedAt.Valid || !scope.now.Before(scope.stored.expiresAt) ||
		contract.Terminal(string(scope.current.State)) {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{}, nativeworker.ErrLeaseGone
	}
	if scope.current.Revision != request.ExpectedRunRevision {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{}, run.ErrRevision
	}

	reservation, found, err := lockedNativeUploadReservationByGrant(
		ctx,
		tx,
		request.GrantID,
	)
	if err != nil {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{}, err
	}
	if !found || reservation.LeaseID != request.LeaseID ||
		reservation.AttemptID != request.AttemptID || reservation.RunID != scope.runID ||
		reservation.Slot != request.Slot || reservation.Size != request.Size ||
		reservation.SHA256 != request.SHA256 {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{},
			nativeworker.ErrUploadGrantMismatch
	}

	slot, err := nativePlannedArtifactSlot(
		scope.stored.plan, protocolVersion,
		reservation.Slot,
		reservation.MediaType,
		reservation.Size,
	)
	if err != nil {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{}, err
	}
	candidate := nativeworker.ArtifactCandidate{
		Reservation: reservation,
		Kind:        slot.Kind,
		Format:      slot.Format,
	}
	if !candidate.Valid() {
		return nativeworker.ArtifactCandidate{}, nativeRenewalScope{},
			errors.New("invalid native artifact candidate")
	}

	return candidate, scope, nil
}

func lockedNativeUploadReservationByGrant(
	ctx context.Context,
	tx *sql.Tx,
	grantID string,
) (nativeworker.UploadReservation, bool, error) {
	var reservation nativeworker.UploadReservation
	var checksum []byte
	err := tx.QueryRowContext(ctx, `
		SELECT grant_id::text,lease_id::text,attempt_id::text,run_id,slot,object_key,
			declared_size,maximum_bytes,sha256,media_type,issued_at,expires_at
		FROM perfeng_control.native_upload_grants
		WHERE grant_id=$1
		FOR UPDATE
	`, grantID).Scan(
		&reservation.GrantID,
		&reservation.LeaseID,
		&reservation.AttemptID,
		&reservation.RunID,
		&reservation.Slot,
		&reservation.ObjectKey,
		&reservation.Size,
		&reservation.MaximumBytes,
		&checksum,
		&reservation.MediaType,
		&reservation.IssuedAt,
		&reservation.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeworker.UploadReservation{}, false, nil
	}
	if err != nil {
		return nativeworker.UploadReservation{}, false, storageError(err)
	}
	reservation.SHA256 = hex.EncodeToString(checksum)
	reservation.IssuedAt = reservation.IssuedAt.UTC()
	reservation.ExpiresAt = reservation.ExpiresAt.UTC()
	if !reservation.Valid() {
		return nativeworker.UploadReservation{}, false,
			errors.New("invalid stored native upload reservation")
	}

	return reservation, true, nil
}

func lockedNativeArtifact(
	ctx context.Context,
	tx *sql.Tx,
	grantID string,
) (nativeworker.ArtifactReference, bool, error) {
	var reference nativeworker.ArtifactReference
	var checksum []byte
	err := tx.QueryRowContext(ctx, `
		SELECT a.artifact_id::text,g.run_id,g.attempt_id::text,g.slot,a.kind,a.uri,
			g.declared_size,g.sha256,g.media_type,a.registered_at,a.format
		FROM perfeng_control.native_artifacts AS a
		JOIN perfeng_control.native_upload_grants AS g ON g.grant_id=a.grant_id
		WHERE a.grant_id=$1
		FOR UPDATE OF a
	`, grantID).Scan(
		&reference.ArtifactID,
		&reference.RunID,
		&reference.AttemptID,
		&reference.Slot,
		&reference.Kind,
		&reference.URI,
		&reference.Size,
		&checksum,
		&reference.MediaType,
		&reference.RegisteredAt,
		&reference.Format,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeworker.ArtifactReference{}, false, nil
	}
	if err != nil {
		return nativeworker.ArtifactReference{}, false, storageError(err)
	}
	reference.SHA256 = hex.EncodeToString(checksum)
	reference.RegisteredAt = reference.RegisteredAt.UTC()
	if !reference.Valid() {
		return nativeworker.ArtifactReference{}, false,
			errors.New("invalid stored native artifact reference")
	}

	return reference, true, nil
}

func nativeArtifactMatches(
	reference nativeworker.ArtifactReference,
	verified nativeworker.VerifiedArtifact,
) bool {
	candidate := verified.Candidate
	reservation := candidate.Reservation

	return reference.RunID == reservation.RunID &&
		reference.AttemptID == reservation.AttemptID && reference.Slot == reservation.Slot &&
		reference.Kind == candidate.Kind && reference.URI == verified.URI &&
		reference.Size == reservation.Size && reference.SHA256 == reservation.SHA256 &&
		reference.MediaType == reservation.MediaType && reference.Format == candidate.Format
}
