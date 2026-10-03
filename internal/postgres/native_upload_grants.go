package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ nativeworker.UploadGrantStore = (*Repository)(nil)

// ReserveNativeUploadGrant authenticates current lease authority and persists
// only the non-secret declaration under the worker-before-Run-before-lease lock
// order. Object-store signing remains outside this transaction.
func (r *Repository) ReserveNativeUploadGrant(
	ctx context.Context,
	workerID string,
	request nativeworker.UploadRequest,
	ttl time.Duration,
) (nativeworker.UploadReservation, error) {
	return r.reserveNativeUploadGrant(ctx, workerID, request, ttl, nativeLeaseProtocolV1)
}

// ReserveNativeBaselineUploadGrant reserves a declared v2 artifact under the
// current v2 lease and its persisted baseline plan.
func (r *Repository) ReserveNativeBaselineUploadGrant(
	ctx context.Context,
	workerID string,
	request nativeworker.UploadRequest,
	ttl time.Duration,
) (nativeworker.UploadReservation, error) {
	return r.reserveNativeUploadGrant(ctx, workerID, request, ttl, nativeLeaseProtocolV2)
}

func (r *Repository) reserveNativeUploadGrant(
	ctx context.Context,
	workerID string,
	request nativeworker.UploadRequest,
	ttl time.Duration,
	protocolVersion int,
) (nativeworker.UploadReservation, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() ||
		!nativeworker.ValidUploadGrantTTL(ttl) {
		return nativeworker.UploadReservation{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}
	defer tx.Rollback()

	scope, err := r.lockNativeRenewalScopeVersion(ctx, tx, workerID, request.LeaseID, protocolVersion)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}
	reservation, err := nativeUploadReservation(scope, workerID, request, ttl, protocolVersion)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}
	reservation, err = persistNativeUploadReservation(ctx, tx, reservation)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}
	if !reservation.Valid() {
		return nativeworker.UploadReservation{}, errors.New("invalid native upload reservation")
	}
	if err = tx.Commit(); err != nil {
		return nativeworker.UploadReservation{}, storageError(err)
	}

	return reservation, nil
}

func nativeUploadReservation(
	scope nativeRenewalScope,
	workerID string,
	request nativeworker.UploadRequest,
	ttl time.Duration,
	protocolVersion int,
) (nativeworker.UploadReservation, error) {
	if !matchingNativeLeaseAuthority(
		scope.stored,
		workerID,
		request.LeaseID,
		request.AttemptID,
		request.Capability,
	) || scope.stored.releasedAt.Valid || !scope.now.Before(scope.stored.expiresAt) ||
		contract.Terminal(string(scope.current.State)) {
		return nativeworker.UploadReservation{}, nativeworker.ErrLeaseGone
	}
	if scope.current.Revision != request.ExpectedRunRevision {
		return nativeworker.UploadReservation{}, run.ErrRevision
	}

	slot, err := nativePlannedArtifactSlot(
		scope.stored.plan, protocolVersion, request.Slot, request.MediaType, request.Size,
	)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}

	expiresAt := scope.now.Add(ttl)
	if expiresAt.After(scope.stored.expiresAt) {
		expiresAt = scope.stored.expiresAt
	}

	return nativeworker.UploadReservation{
		LeaseID: request.LeaseID, AttemptID: request.AttemptID, RunID: scope.runID,
		Slot: request.Slot,
		ObjectKey: fmt.Sprintf(
			"runs/%s/attempts/%s/%s",
			scope.runID,
			request.AttemptID,
			request.Slot,
		),
		Size: request.Size, MaximumBytes: slot.MaximumBytes, SHA256: request.SHA256,
		MediaType: request.MediaType, IssuedAt: scope.now, ExpiresAt: expiresAt,
	}, nil
}

func nativePlannedArtifactSlot(
	storedPlan []byte,
	protocolVersion int,
	name string,
	mediaType string,
	size int64,
) (nativeworker.ArtifactSlot, error) {
	var slot nativeworker.ArtifactSlot
	var matched bool
	switch protocolVersion {
	case nativeLeaseProtocolV1:
		var plan nativeworker.Plan
		if err := json.Unmarshal(storedPlan, &plan); err != nil || plan.Validate() != nil {
			return slot, errors.New("invalid stored native execution plan")
		}
		slot, matched = plan.MatchArtifactSlot(name, mediaType, size)
	case nativeLeaseProtocolV2:
		var plan nativeworker.BaselinePlan
		if err := json.Unmarshal(storedPlan, &plan); err != nil || plan.Validate() != nil {
			return slot, errors.New("invalid stored native baseline plan")
		}
		slot, matched = plan.MatchArtifactSlot(name, mediaType, size)
	default:
		return slot, run.ErrValidation
	}
	if !matched {
		return slot, nativeworker.ErrArtifactPlan
	}

	return slot, nil
}

func persistNativeUploadReservation(
	ctx context.Context,
	tx *sql.Tx,
	reservation nativeworker.UploadReservation,
) (nativeworker.UploadReservation, error) {
	existing, found, err := lockedNativeUploadReservation(
		ctx,
		tx,
		reservation.LeaseID,
		reservation.Slot,
	)
	if err != nil {
		return nativeworker.UploadReservation{}, err
	}
	if found {
		if !sameNativeUploadDeclaration(existing, reservation) {
			return nativeworker.UploadReservation{}, run.ErrConflict
		}
		reservation.GrantID = existing.GrantID
		if _, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.native_upload_grants
			SET issued_at=$1,expires_at=$2
			WHERE grant_id=$3
		`, reservation.IssuedAt, reservation.ExpiresAt, reservation.GrantID); err != nil {
			return nativeworker.UploadReservation{}, storageError(err)
		}
	} else {
		reservation.GrantID, err = newUUID()
		if err != nil {
			return nativeworker.UploadReservation{}, run.ErrUnavailable
		}
		checksum, decodeErr := hex.DecodeString(reservation.SHA256)
		if decodeErr != nil {
			return nativeworker.UploadReservation{}, run.ErrValidation
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO perfeng_control.native_upload_grants(
				grant_id,run_id,lease_id,attempt_id,slot,object_key,
				declared_size,maximum_bytes,sha256,media_type,issued_at,expires_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, reservation.GrantID, reservation.RunID, reservation.LeaseID,
			reservation.AttemptID, reservation.Slot, reservation.ObjectKey,
			reservation.Size, reservation.MaximumBytes, checksum, reservation.MediaType,
			reservation.IssuedAt, reservation.ExpiresAt); err != nil {
			return nativeworker.UploadReservation{}, storageError(err)
		}
	}

	return reservation, nil
}

func lockedNativeUploadReservation(
	ctx context.Context,
	tx *sql.Tx,
	leaseID string,
	slot string,
) (nativeworker.UploadReservation, bool, error) {
	var reservation nativeworker.UploadReservation
	var checksum []byte
	err := tx.QueryRowContext(ctx, `
		SELECT grant_id::text,lease_id::text,attempt_id::text,run_id,slot,object_key,
			declared_size,maximum_bytes,sha256,media_type,issued_at,expires_at
		FROM perfeng_control.native_upload_grants
		WHERE lease_id=$1 AND slot=$2
		FOR UPDATE
	`, leaseID, slot).Scan(
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

func sameNativeUploadDeclaration(
	stored nativeworker.UploadReservation,
	requested nativeworker.UploadReservation,
) bool {
	return stored.LeaseID == requested.LeaseID && stored.AttemptID == requested.AttemptID &&
		stored.RunID == requested.RunID && stored.Slot == requested.Slot &&
		stored.ObjectKey == requested.ObjectKey && stored.Size == requested.Size &&
		stored.MaximumBytes == requested.MaximumBytes && stored.SHA256 == requested.SHA256 &&
		stored.MediaType == requested.MediaType
}
