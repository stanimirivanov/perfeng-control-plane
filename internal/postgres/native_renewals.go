package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ nativeworker.RenewalStore = (*Repository)(nil)

type storedNativeLease struct {
	leaseID          string
	attemptID        string
	workerID         string
	capabilityDigest []byte
	expiresAt        time.Time
	renewAfter       time.Duration
}

type nativeRenewalScope struct {
	runID   string
	current run.Run
	stored  storedNativeLease
	now     time.Time
}

// RenewNativeLease serializes worker authority, Run lifecycle and lease expiry
// under the established worker-before-Run lock order.
func (r *Repository) RenewNativeLease(
	ctx context.Context,
	workerID string,
	request nativeworker.RenewalRequest,
	ttl time.Duration,
	cancellationGrace time.Duration,
) (nativeworker.Renewal, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() ||
		!run.ValidLeaseTTL(ttl) ||
		!nativeworker.ValidCancellationGrace(cancellationGrace, ttl) {
		return nativeworker.Renewal{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.Renewal{}, err
	}
	defer tx.Rollback()

	scope, err := r.lockNativeRenewalScope(ctx, tx, workerID, request.LeaseID)
	if err != nil {
		return nativeworker.Renewal{}, err
	}
	if !currentNativeLease(scope.stored, workerID, request, scope.now) {
		return nativeworker.Renewal{}, nativeworker.ErrLeaseGone
	}
	if scope.current.Revision != request.ExpectedRunRevision {
		return nativeworker.Renewal{}, run.ErrRevision
	}
	if contract.Terminal(string(scope.current.State)) {
		return nativeworker.Renewal{}, nativeworker.ErrLeaseGone
	}
	if !nativeworker.ValidLeasePolicy(ttl, scope.stored.renewAfter) {
		return nativeworker.Renewal{}, errors.New("invalid stored native lease policy")
	}

	next := scope.current
	cancellationRequested := scope.current.State == run.StateCancelling
	if !cancellationRequested {
		next, err = nativeworker.AdvancePhase(
			scope.current,
			scope.current.Revision,
			request.Phase,
			scope.now,
		)
		if err != nil {
			return nativeworker.Renewal{}, err
		}
	}
	if next.Revision != scope.current.Revision {
		if err = updateNativeRun(ctx, tx, next); err != nil {
			return nativeworker.Renewal{}, err
		}
	}

	expiresAt := scope.now.Add(ttl)
	if _, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.native_execution_leases
		SET expires_at=$1
		WHERE run_id=$2
	`, expiresAt, scope.runID); err != nil {
		return nativeworker.Renewal{}, storageError(err)
	}

	renewal := nativeworker.Renewal{
		LeaseID: request.LeaseID, AttemptID: request.AttemptID,
		RunRevision: next.Revision, ServerTime: scope.now, ExpiresAt: expiresAt,
		RenewAfterSeconds:     int(scope.stored.renewAfter / time.Second),
		CancellationRequested: cancellationRequested,
	}
	if cancellationRequested {
		stopBy := scope.now.Add(cancellationGrace)
		renewal.StopBy = &stopBy
	}
	if !renewal.Valid() {
		return nativeworker.Renewal{}, errors.New("invalid native lease renewal")
	}
	if err = tx.Commit(); err != nil {
		return nativeworker.Renewal{}, storageError(err)
	}

	return renewal, nil
}

// lockNativeRenewalScope preserves the worker-before-Run-before-lease order and
// reads database time only after all current authority is serialized.
func (r *Repository) lockNativeRenewalScope(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	leaseID string,
) (nativeRenewalScope, error) {
	runID, leasedWorker, err := nativeLeaseLocation(ctx, tx, leaseID)
	if err != nil {
		return nativeRenewalScope{}, err
	}
	if leasedWorker != workerID {
		return nativeRenewalScope{}, nativeworker.ErrLeaseGone
	}
	if err = lockNativeWorker(ctx, tx, workerID); err != nil {
		return nativeRenewalScope{}, err
	}
	current, target, _, err := lockNativeRun(ctx, tx, runID)
	if err != nil {
		return nativeRenewalScope{}, err
	}
	if current.ID == "" {
		return nativeRenewalScope{}, nativeworker.ErrLeaseGone
	}
	if current.ID != runID || target != run.ExecutionTargetNativeWindows {
		return nativeRenewalScope{}, errors.New("invalid stored native execution target")
	}
	stored, err := lockedNativeLease(ctx, tx, runID)
	if err != nil {
		return nativeRenewalScope{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nativeRenewalScope{}, err
	}

	return nativeRenewalScope{runID: runID, current: current, stored: stored, now: now}, nil
}

func nativeLeaseLocation(
	ctx context.Context,
	tx *sql.Tx,
	leaseID string,
) (string, string, error) {
	var runID string
	var workerID string
	err := tx.QueryRowContext(ctx, `
		SELECT run_id,worker_id
		FROM perfeng_control.native_execution_leases
		WHERE lease_id=$1
	`, leaseID).Scan(&runID, &workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nativeworker.ErrLeaseGone
	}
	if err != nil {
		return "", "", storageError(err)
	}

	return runID, workerID, nil
}

// lockNativeWorker establishes the first lock in every native lease mutation.
func lockNativeWorker(ctx context.Context, tx *sql.Tx, workerID string) error {
	var locked string
	err := tx.QueryRowContext(ctx, `
		SELECT worker_id
		FROM perfeng_control.native_worker_presence
		WHERE worker_id=$1
		FOR UPDATE
	`, workerID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeworker.ErrLeaseGone
	}
	if err != nil {
		return storageError(err)
	}

	return nil
}

func lockedNativeLease(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (storedNativeLease, error) {
	var stored storedNativeLease
	var renewAfterSeconds int
	err := tx.QueryRowContext(ctx, `
		SELECT lease_id::text,attempt_id::text,worker_id,capability_sha256,
			expires_at,renew_after_seconds
		FROM perfeng_control.native_execution_leases
		WHERE run_id=$1
		FOR UPDATE
	`, runID).Scan(
		&stored.leaseID,
		&stored.attemptID,
		&stored.workerID,
		&stored.capabilityDigest,
		&stored.expiresAt,
		&renewAfterSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedNativeLease{}, nativeworker.ErrLeaseGone
	}
	if err != nil {
		return storedNativeLease{}, storageError(err)
	}
	stored.expiresAt = stored.expiresAt.UTC()
	stored.renewAfter = time.Duration(renewAfterSeconds) * time.Second

	return stored, nil
}

func currentNativeLease(
	stored storedNativeLease,
	workerID string,
	request nativeworker.RenewalRequest,
	now time.Time,
) bool {
	digest := sha256.Sum256([]byte(request.Capability))

	return stored.leaseID == request.LeaseID && stored.attemptID == request.AttemptID &&
		stored.workerID == workerID && now.Before(stored.expiresAt) &&
		subtle.ConstantTimeCompare(stored.capabilityDigest, digest[:]) == 1
}

func updateNativeRun(ctx context.Context, tx *sql.Tx, next run.Run) error {
	snapshot, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.runs
		SET snapshot=$1
		WHERE run_id=$2
	`, snapshot, next.ID); err != nil {
		return storageError(err)
	}

	return nil
}
