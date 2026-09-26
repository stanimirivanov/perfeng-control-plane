package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ nativeworker.ReleaseStore = (*Repository)(nil)

// ReleaseNativeLease expires current authority and records the first release
// reason under the worker-before-Run-before-lease lock order.
func (r *Repository) ReleaseNativeLease(
	ctx context.Context,
	workerID string,
	request nativeworker.ReleaseRequest,
) error {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	scope, err := r.lockNativeRenewalScope(ctx, tx, workerID, request.LeaseID)
	if err != nil {
		return err
	}
	if !matchingNativeLeaseAuthority(
		scope.stored,
		workerID,
		request.LeaseID,
		request.AttemptID,
		request.Capability,
	) {
		return nativeworker.ErrLeaseGone
	}
	if scope.stored.releasedAt.Valid {
		if scope.stored.releaseReason.String == string(request.Reason) {
			return nil
		}

		return run.ErrConflict
	}
	if !scope.now.Before(scope.stored.expiresAt) || contract.Terminal(string(scope.current.State)) {
		return nativeworker.ErrLeaseGone
	}
	if scope.current.Revision != request.ExpectedRunRevision {
		return run.ErrRevision
	}

	next, err := releasedNativeRun(scope.current, request.Reason, scope.now)
	if err != nil {
		return err
	}
	if next.Revision != scope.current.Revision {
		if err = updateNativeRun(ctx, tx, next); err != nil {
			return err
		}
	}

	message := sql.NullString{String: request.Message, Valid: request.Message != ""}
	if _, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.native_execution_leases
		SET expires_at=$1,released_at=$1,release_reason=$2,release_message=$3
		WHERE run_id=$4
	`, scope.now, request.Reason, message, scope.runID); err != nil {
		return storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return storageError(err)
	}

	return nil
}

func releasedNativeRun(
	current run.Run,
	reason nativeworker.ReleaseReason,
	now time.Time,
) (run.Run, error) {
	if reason == nativeworker.ReleaseCancellationRequested {
		if current.State != run.StateCancelling {
			return run.Run{}, run.ErrConflict
		}

		return current.Transition(
			current.Revision,
			run.Change{State: run.StateAborted},
			now,
		)
	}
	if current.State == run.StateCancelling {
		return run.Run{}, run.ErrConflict
	}

	return current.Clone(), nil
}
