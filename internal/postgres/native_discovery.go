package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// lockNativeWorkerIdentity serializes a v2 identity even before its first
// presence report exists. Acquire it before any presence, Run or lease row lock.
// Hash collisions only serialize unrelated identities; full keys still scope SQL.
func lockNativeWorkerIdentity(ctx context.Context, tx *sql.Tx, workerID string) error {
	_, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended('native-worker/v2:' || $1, 0))
	`, workerID)

	return storageError(err)
}

// DiscoverNativeBaselineAttempt observes a worker's current v2 measurement slot.
// It holds the worker identity and presence locks, then reads a fresh statement
// snapshot and clock. No durable record is changed and no capability is selected.
func (r *Repository) DiscoverNativeBaselineAttempt(ctx context.Context, workerID string) (nativeworker.Discovery, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) {
		return nativeworker.Discovery{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.Discovery{}, err
	}
	defer tx.Rollback()

	if err := lockNativeWorkerCapacity(ctx, tx, workerID); err != nil {
		return nativeworker.Discovery{}, err
	}

	rows, err := tx.QueryContext(ctx, `
		WITH observation AS (SELECT statement_timestamp() AS server_time)
		SELECT observation.server_time, p.availability,
			l.run_id, l.attempt_id::text, l.lease_id::text, l.expires_at,
			t.kind, t.resulting_run_revision, t.accepted_at,
			r.execution_target, r.snapshot
		FROM observation
		LEFT JOIN perfeng_control.native_worker_presence_v2 AS p ON p.worker_id=$1
		LEFT JOIN LATERAL (
			SELECT run_id, attempt_id, lease_id, expires_at
			FROM perfeng_control.native_execution_leases
			WHERE worker_id=$1 AND protocol_version=2 AND released_at IS NULL
				AND expires_at > observation.server_time
			LIMIT 2
		) AS l ON true
		LEFT JOIN perfeng_control.native_terminal_reports AS t
			ON t.run_id=l.run_id AND t.attempt_id=l.attempt_id AND t.worker_v2_id=$1
		LEFT JOIN perfeng_control.runs AS r ON r.run_id=l.run_id
	`, workerID)
	if err != nil {
		return nativeworker.Discovery{}, storageError(err)
	}
	defer rows.Close()

	var discovery nativeworker.Discovery
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return nativeworker.Discovery{}, errors.New("multiple current native worker slots")
		}
		discovery, err = scanNativeDiscovery(rows)
		if err != nil {
			return nativeworker.Discovery{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return nativeworker.Discovery{}, storageError(err)
	}
	if err := rows.Close(); err != nil {
		return nativeworker.Discovery{}, storageError(err)
	}
	if count != 1 || !discovery.Valid() {
		return nativeworker.Discovery{}, errors.New("invalid stored native discovery")
	}
	if err := tx.Commit(); err != nil {
		return nativeworker.Discovery{}, storageError(err)
	}

	return discovery, nil
}

func scanNativeDiscovery(rows *sql.Rows) (nativeworker.Discovery, error) {
	var now time.Time
	var availability, runID, attemptID, leaseID, kind, target sql.NullString
	var expiry, acceptedAt sql.NullTime
	var revision sql.NullInt64
	var snapshot []byte
	if err := rows.Scan(&now, &availability, &runID, &attemptID, &leaseID, &expiry,
		&kind, &revision, &acceptedAt, &target, &snapshot); err != nil {
		return nativeworker.Discovery{}, storageError(err)
	}
	discovery := nativeworker.Discovery{
		State: nativeworker.DiscoveryNone, ServerTime: now.UTC(), Availability: "UNREPORTED",
	}
	if availability.Valid {
		discovery.Availability = availability.String
	}
	if !runID.Valid {
		return discovery, nil
	}
	if !availability.Valid || !attemptID.Valid || !leaseID.Valid || !expiry.Valid ||
		!target.Valid || run.ExecutionTarget(target.String) != run.ExecutionTargetNativeWindows {
		return nativeworker.Discovery{}, errors.New("inconsistent stored native worker slot")
	}
	slotExpiry := expiry.Time.UTC()
	discovery.State = nativeworker.DiscoveryFound
	discovery.SlotExpiresAt = &slotExpiry
	inspection := nativeworker.Inspection{
		RunID: runID.String, AttemptID: attemptID.String, ServerTime: discovery.ServerTime,
	}
	if kind.Valid {
		if !revision.Valid || !acceptedAt.Valid {
			return nativeworker.Discovery{}, errors.New("invalid stored native terminal report")
		}
		accepted := acceptedAt.Time.UTC()
		inspection.State = nativeworker.InspectionTerminal
		inspection.TerminalKind = nativeworker.TerminalKind(kind.String)
		inspection.RunRevision = revision.Int64
		inspection.AcceptedAt = &accepted
	} else {
		current, err := decodeRun(snapshot)
		if err != nil || current.ID != runID.String {
			return nativeworker.Discovery{}, errors.New("invalid stored native Run snapshot")
		}
		inspection.State = nativeworker.InspectionActive
		inspection.LeaseID = leaseID.String
		inspection.ExpiresAt = &slotExpiry
		inspection.RunRevision = current.Revision
	}
	discovery.Attempt = &inspection
	return discovery, nil
}
