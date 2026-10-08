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

// InspectNativeBaselineAttempt observes one authenticated worker's known v2
// attempt. A single statement supplies one database snapshot and server clock;
// it does not lock or renew authority.
func (r *Repository) InspectNativeBaselineAttempt(
	ctx context.Context,
	workerID string,
	request nativeworker.InspectionRequest,
) (nativeworker.Inspection, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return nativeworker.Inspection{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	var observedAt time.Time
	var terminalKind sql.NullString
	var terminalRevision sql.NullInt64
	var acceptedAt sql.NullTime
	var leaseID sql.NullString
	var expiresAt sql.NullTime
	var releasedAt sql.NullTime
	var snapshot []byte
	err := r.db.QueryRowContext(ctx, `
		WITH observation AS (SELECT statement_timestamp() AS server_time)
		SELECT observation.server_time,
			t.kind,t.resulting_run_revision,t.accepted_at,
			l.lease_id::text,l.expires_at,l.released_at,r.snapshot
		FROM observation
		LEFT JOIN perfeng_control.native_terminal_reports AS t
			ON t.run_id=$1 AND t.attempt_id=$2 AND t.worker_v2_id=$3
		LEFT JOIN perfeng_control.native_execution_leases AS l
			ON l.run_id=$1 AND l.attempt_id=$2 AND l.worker_v2_id=$3
		LEFT JOIN perfeng_control.runs AS r ON r.run_id=$1
	`, request.RunID, request.AttemptID, workerID).Scan(
		&observedAt,
		&terminalKind, &terminalRevision, &acceptedAt,
		&leaseID, &expiresAt, &releasedAt, &snapshot,
	)
	if err != nil {
		return nativeworker.Inspection{}, storageError(err)
	}
	inspection := nativeworker.Inspection{
		RunID: request.RunID, AttemptID: request.AttemptID,
		ServerTime: observedAt.UTC(),
	}
	if terminalKind.Valid {
		if !terminalRevision.Valid || !acceptedAt.Valid {
			return nativeworker.Inspection{}, errors.New("invalid stored native terminal report")
		}
		accepted := acceptedAt.Time.UTC()
		inspection.State = nativeworker.InspectionTerminal
		inspection.TerminalKind = nativeworker.TerminalKind(terminalKind.String)
		inspection.RunRevision = terminalRevision.Int64
		inspection.AcceptedAt = &accepted
	} else if leaseID.Valid {
		if !expiresAt.Valid {
			return nativeworker.Inspection{}, errors.New("invalid stored native lease")
		}
		if releasedAt.Valid || !observedAt.Before(expiresAt.Time) {
			inspection.State = nativeworker.InspectionInactive
		} else {
			current, decodeErr := decodeRun(snapshot)
			if decodeErr != nil || current.ID != request.RunID {
				return nativeworker.Inspection{}, errors.New("invalid stored native Run snapshot")
			}
			expiry := expiresAt.Time.UTC()
			inspection.State = nativeworker.InspectionActive
			inspection.LeaseID = leaseID.String
			inspection.ExpiresAt = &expiry
			inspection.RunRevision = current.Revision
		}
	} else {
		return nativeworker.Inspection{}, nativeworker.ErrAttemptNotFound
	}
	if !inspection.Valid() {
		return nativeworker.Inspection{}, errors.New("invalid stored native attempt inspection")
	}

	return inspection, nil
}
