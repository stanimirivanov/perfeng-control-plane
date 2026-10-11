package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const nativeLeaseProtocolV2 = 2

type nativeLeaseAuthority struct {
	leaseID    string
	attemptID  string
	capability string
	issuedAt   time.Time
	expiresAt  time.Time
}

// NativeCandidates returns isolated CREATED native Run snapshots. Final
// eligibility is decided under locks by AcquireNativeBaselineLease.
func (r *Repository) NativeCandidates(
	ctx context.Context,
	limit int,
) ([]nativeworker.Candidate, error) {
	if !nativeworker.ValidCandidateLimit(limit) {
		return nil, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(ctx, `
		SELECT r.principal,r.snapshot
		FROM perfeng_control.runs AS r
		LEFT JOIN perfeng_control.native_execution_leases AS l
			ON l.run_id=r.run_id
		WHERE r.execution_target='NATIVE_WINDOWS'
		AND r.snapshot->>'state'='CREATED'
		AND (
			l.run_id IS NULL
			OR l.released_at IS NOT NULL
			OR l.expires_at <= clock_timestamp()
		)
		ORDER BY r.run_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, storageError(err)
	}

	candidates := make([]nativeworker.Candidate, 0, limit)
	for rows.Next() {
		var principal string
		var snapshotBytes []byte
		if err = rows.Scan(&principal, &snapshotBytes); err != nil {
			break
		}
		snapshot, decodeErr := decodeRun(snapshotBytes)
		if decodeErr != nil {
			err = decodeErr
			break
		}
		candidates = append(candidates, nativeworker.Candidate{
			Principal: principal, Run: snapshot,
		})
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, storageError(rowErr)
	}
	if closeErr != nil {
		return nil, storageError(closeErr)
	}

	return candidates, nil
}

// lockNativeWorkerCapacity serializes acquisitions for one worker ID before
// the transaction locks a Run.
func lockNativeWorkerCapacity(ctx context.Context, tx *sql.Tx, workerID string) error {
	if err := lockNativeWorkerIdentity(ctx, tx, workerID); err != nil {
		return err
	}
	var lockedID string
	err := tx.QueryRowContext(ctx, `
		SELECT worker_id FROM perfeng_control.native_worker_presence_v2
		WHERE worker_id=$1 FOR UPDATE
	`, workerID).Scan(&lockedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storageError(err)
	}

	return nil
}

// lockNativeRun holds the selected Run row after the worker row until the
// transaction completes.
func lockNativeRun(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (run.Run, run.ExecutionTarget, string, error) {
	var principal string
	var target run.ExecutionTarget
	var snapshotBytes []byte
	err := tx.QueryRowContext(ctx, `
		SELECT principal,execution_target,snapshot
		FROM perfeng_control.runs
		WHERE run_id=$1
		FOR UPDATE
	`, runID).Scan(&principal, &target, &snapshotBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, "", "", nil
	}
	if err != nil {
		return run.Run{}, "", "", storageError(err)
	}
	current, err := decodeRun(snapshotBytes)
	if err != nil {
		return run.Run{}, "", "", err
	}

	return current, target, principal, nil
}

func nativeWorkerOccupied(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now time.Time,
) (bool, error) {
	var occupied bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM perfeng_control.native_execution_leases
			WHERE worker_id=$1 AND released_at IS NULL AND expires_at>$2
		)
	`, workerID, now).Scan(&occupied)

	return occupied, storageError(err)
}

func nativeRunLeased(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	now time.Time,
) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM perfeng_control.native_execution_leases
			WHERE run_id=$1 AND released_at IS NULL AND expires_at>$2
		)
	`, runID, now).Scan(&active)

	return active, storageError(err)
}

// persistNativeLease requires locked worker and Run rows and a validated plan.
func persistNativeLease(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	runID string,
	planBytes []byte,
	now time.Time,
	ttl time.Duration,
	renewAfter time.Duration,
) (nativeLeaseAuthority, bool, error) {
	leaseID, err := newUUID()
	if err != nil {
		return nativeLeaseAuthority{}, false, run.ErrUnavailable
	}
	attemptID, err := newUUID()
	if err != nil {
		return nativeLeaseAuthority{}, false, run.ErrUnavailable
	}
	capability, capabilityDigest, err := newNativeLeaseCapability()
	if err != nil {
		return nativeLeaseAuthority{}, false, run.ErrUnavailable
	}
	expiresAt := now.Add(ttl)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.native_execution_leases(
			run_id,lease_id,attempt_id,worker_id,protocol_version,capability_sha256,
			issued_at,expires_at,renew_after_seconds,plan
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (run_id) DO UPDATE SET
			lease_id=EXCLUDED.lease_id,
			attempt_id=EXCLUDED.attempt_id,
			worker_id=EXCLUDED.worker_id,
			protocol_version=EXCLUDED.protocol_version,
			capability_sha256=EXCLUDED.capability_sha256,
			issued_at=EXCLUDED.issued_at,
			expires_at=EXCLUDED.expires_at,
			renew_after_seconds=EXCLUDED.renew_after_seconds,
			plan=EXCLUDED.plan,
			released_at=NULL,
			release_reason=NULL,
			release_message=NULL
		WHERE perfeng_control.native_execution_leases.released_at IS NOT NULL
		OR perfeng_control.native_execution_leases.expires_at <= EXCLUDED.issued_at
	`, runID, leaseID, attemptID, workerID, nativeLeaseProtocolV2, capabilityDigest[:], now, expiresAt,
		int(renewAfter/time.Second), planBytes)
	if err != nil {
		return nativeLeaseAuthority{}, false, storageError(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nativeLeaseAuthority{}, false, storageError(err)
	}
	if changed != 1 {
		return nativeLeaseAuthority{}, false, nil
	}

	return nativeLeaseAuthority{
		leaseID: leaseID, attemptID: attemptID, capability: capability,
		issuedAt: now, expiresAt: expiresAt,
	}, true, nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func newNativeLeaseCapability() (string, [sha256.Size]byte, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	capability := hex.EncodeToString(value[:])

	return capability, sha256.Sum256([]byte(capability)), nil
}
