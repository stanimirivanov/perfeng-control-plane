package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

var _ nativeworker.LeaseStore = (*Repository)(nil)

const (
	nativeLeaseProtocolV1 = 1
	nativeLeaseProtocolV2 = 2
)

type nativeLeaseAuthority struct {
	leaseID    string
	attemptID  string
	capability string
	issuedAt   time.Time
	expiresAt  time.Time
}

// EligibleNativeWorker returns one fresh scheduling observation without locking
// it beyond this read. Acquisition rechecks the exact observation atomically.
func (r *Repository) EligibleNativeWorker(
	ctx context.Context,
	workerID string,
	maximumAge time.Duration,
) (nativeworker.PresenceObservation, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) ||
		!nativeworker.ValidPresenceMaximumAge(maximumAge) {
		return nativeworker.PresenceObservation{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	var observedAt time.Time
	var document []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT observed_at,presence
		FROM perfeng_control.native_worker_presence
		WHERE worker_id=$1
		AND availability='AVAILABLE'
		AND qualified
		AND authorized_to_acquire
		AND observed_at > clock_timestamp() - make_interval(secs => $2)
	`, workerID, int(maximumAge/time.Second)).Scan(&observedAt, &document)
	if err != nil {
		return nativeworker.PresenceObservation{}, storageError(err)
	}
	presence, err := nativeworker.ParsePresence(document, nativeworker.MaximumPresenceBytes)
	if err != nil || presence.Availability != "AVAILABLE" {
		return nativeworker.PresenceObservation{}, errors.New("invalid stored native worker presence")
	}

	return nativeworker.PresenceObservation{
		WorkerID: workerID, ObservedAt: observedAt.UTC(), Presence: presence,
	}, nil
}

// NativeCandidates returns isolated CREATED native Run snapshots. Final
// eligibility is decided under locks by AcquireNativeLease.
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

// AcquireNativeLease locks the worker before the Run, then rechecks presence,
// capacity, compatibility, lifecycle and competing ownership at database time.
func (r *Repository) AcquireNativeLease(
	ctx context.Context,
	workerID string,
	expectedPresenceAt time.Time,
	candidate nativeworker.Candidate,
	plan nativeworker.Plan,
	presenceMaximumAge time.Duration,
	ttl time.Duration,
	renewAfter time.Duration,
) (nativeworker.Lease, bool, error) {
	if !validNativeLeaseRequest(
		workerID, expectedPresenceAt, candidate, plan, presenceMaximumAge, ttl, renewAfter,
	) {
		return nativeworker.Lease{}, false, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	defer tx.Rollback()

	observedAt, presence, eligible, err := lockNativeWorkerPresence(ctx, tx, workerID)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	if !eligible || !observedAt.Equal(expectedPresenceAt) ||
		!presence.Capabilities.Supports(plan) {
		return nativeworker.Lease{}, false, nil
	}

	current, target, principal, err := lockNativeRun(ctx, tx, candidate.Run.ID)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	if now.Sub(observedAt) >= presenceMaximumAge || principal != candidate.Principal ||
		target != run.ExecutionTargetNativeWindows || current.State != run.StateCreated ||
		current.Revision != candidate.Run.Revision || current.Request != candidate.Run.Request ||
		!planMatchesRun(plan, current) {
		return nativeworker.Lease{}, false, nil
	}

	occupied, err := nativeWorkerOccupied(ctx, tx, workerID, now)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	if occupied {
		return nativeworker.Lease{}, false, nil
	}

	active, err := nativeRunLeased(ctx, tx, current.ID, now)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	if active {
		return nativeworker.Lease{}, false, nil
	}

	lease, written, err := writeNativeLease(
		ctx, tx, workerID, current, plan, now, ttl, renewAfter,
	)
	if err != nil {
		return nativeworker.Lease{}, false, err
	}
	if !written {
		return nativeworker.Lease{}, false, nil
	}
	if err = tx.Commit(); err != nil {
		return nativeworker.Lease{}, false, storageError(err)
	}

	return lease, true, nil
}

func validNativeLeaseRequest(
	workerID string,
	expectedPresenceAt time.Time,
	candidate nativeworker.Candidate,
	plan nativeworker.Plan,
	presenceMaximumAge time.Duration,
	ttl time.Duration,
	renewAfter time.Duration,
) bool {
	return contract.ValidNativeWorkerIdentifier(workerID) && !expectedPresenceAt.IsZero() &&
		expectedPresenceAt.Location() == time.UTC && candidate.Principal != "" &&
		contract.ValidID(candidate.Run.ID) && candidate.Run.Revision >= 1 &&
		candidate.Run.State == run.StateCreated && candidate.Run.Request.Validate() == nil &&
		plan.Validate() == nil && nativeworker.ValidPresenceMaximumAge(presenceMaximumAge) &&
		nativeworker.ValidLeasePolicy(ttl, renewAfter)
}

// lockNativeWorkerPresence holds the worker row until transaction completion.
func lockNativeWorkerPresence(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
) (time.Time, nativeworker.Presence, bool, error) {
	var observedAt time.Time
	var availability string
	var qualified bool
	var authorized bool
	var document []byte
	err := tx.QueryRowContext(ctx, `
		SELECT observed_at,availability,qualified,authorized_to_acquire,presence
		FROM perfeng_control.native_worker_presence
		WHERE worker_id=$1
		FOR UPDATE
	`, workerID).Scan(&observedAt, &availability, &qualified, &authorized, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nativeworker.Presence{}, false, nil
	}
	if err != nil {
		return time.Time{}, nativeworker.Presence{}, false, storageError(err)
	}
	presence, err := nativeworker.ParsePresence(document, nativeworker.MaximumPresenceBytes)
	if err != nil || presence.Availability != availability {
		return time.Time{}, nativeworker.Presence{}, false,
			errors.New("invalid stored native worker presence")
	}

	return observedAt.UTC(), presence,
		availability == "AVAILABLE" && qualified && authorized, nil
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

func planMatchesRun(plan nativeworker.Plan, current run.Run) bool {
	return plan.Assignment.CatalogueTestID == current.Request.TestSuite &&
		plan.Assignment.Profile == current.Request.Profile &&
		plan.Assignment.Target.EnvironmentID == current.Request.Environment.ID
}

// writeNativeLease requires locked worker and Run rows. It writes new authority
// only when the previous authority for the Run expired by the supplied database
// time.
func writeNativeLease(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	current run.Run,
	plan nativeworker.Plan,
	now time.Time,
	ttl time.Duration,
	renewAfter time.Duration,
) (nativeworker.Lease, bool, error) {
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return nativeworker.Lease{}, false, run.ErrValidation
	}
	authority, written, err := persistNativeLease(
		ctx, tx, workerID, current.ID, nativeLeaseProtocolV1, planBytes, now, ttl, renewAfter,
	)
	if err != nil || !written {
		return nativeworker.Lease{}, false, err
	}

	return nativeworker.Lease{
		LeaseID: authority.leaseID, AttemptID: authority.attemptID, RunID: current.ID,
		RunRevision: current.Revision, IssuedAt: authority.issuedAt,
		ExpiresAt:         authority.expiresAt,
		RenewAfterSeconds: int(renewAfter / time.Second), Plan: plan.Clone(),
		Capability: authority.capability,
	}, true, nil
}

// persistNativeLease requires locked worker and Run rows and a validated plan.
// It retains one Run authority across both worker protocol generations.
func persistNativeLease(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	runID string,
	protocolVersion int,
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
	`, runID, leaseID, attemptID, workerID, protocolVersion, capabilityDigest[:], now, expiresAt,
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
