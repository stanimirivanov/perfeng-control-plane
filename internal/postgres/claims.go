package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ run.ReconciliationStore = (*Repository)(nil)

type claimCandidate struct {
	principal string
	snapshot  run.Run
}

// ClaimRuns holds Run row locks while it rechecks and writes leases in one
// transaction. Contention can produce a short batch; this call does not refill it.
func (r *Repository) ClaimRuns(
	ctx context.Context,
	workerID string,
	limit int,
	ttl time.Duration,
) ([]run.Claim, error) {
	if !run.ValidClaimOptions(workerID, limit, ttl) {
		return nil, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	candidates, err := lockClaimCandidates(ctx, tx, limit)
	if err != nil {
		return nil, err
	}

	claims := make([]run.Claim, 0, len(candidates))
	for _, candidate := range candidates {
		claim, acquired, err := tryAcquireClaim(ctx, tx, candidate, workerID, ttl)
		if err != nil {
			return nil, err
		}
		if acquired {
			claims = append(claims, claim)
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, storageError(err)
	}

	return claims, nil
}

// lockClaimCandidates locks each returned Run row until tx ends. It closes the
// query rows before callers issue another statement on the same transaction.
func lockClaimCandidates(
	ctx context.Context,
	tx *sql.Tx,
	limit int,
) ([]claimCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			r.principal,
			r.snapshot
		FROM perfeng_control.runs AS r
		LEFT JOIN perfeng_control.reconciliation_leases AS l
			ON l.run_id = r.run_id
		WHERE r.snapshot ->> 'state' IN (
			'CREATED',
			'VALIDATING',
			'PROVISIONING',
			'WARMING_UP',
			'RUNNING',
			'COLLECTING',
			'ANALYZING',
			'REPORTING',
			'CANCELLING'
		)
		AND (
			l.run_id IS NULL
			OR (
				l.expires_at <= clock_timestamp()
				AND (
					l.available_at <= clock_timestamp()
					OR r.snapshot ->> 'state' = 'CANCELLING'
				)
			)
		)
		ORDER BY
			(r.snapshot ->> 'state' = 'CANCELLING') DESC,
			l.available_at NULLS FIRST,
			r.run_id
		LIMIT $1
		FOR UPDATE OF r SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, storageError(err)
	}

	candidates := make([]claimCandidate, 0, limit)
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

		candidates = append(candidates, claimCandidate{
			principal: principal,
			snapshot:  snapshot,
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

// tryAcquireClaim rechecks lease eligibility in a fresh READ COMMITTED statement.
// The candidate query can have an older join snapshot after waiting for a Run lock.
// Reading database time here also gives every candidate its own lease deadline.
func tryAcquireClaim(
	ctx context.Context,
	tx *sql.Tx,
	candidate claimCandidate,
	workerID string,
	ttl time.Duration,
) (run.Claim, bool, error) {
	now, err := dbNow(ctx, tx)
	if err != nil {
		return run.Claim{}, false, err
	}

	var expiresAt time.Time
	var availableAt time.Time

	err = tx.QueryRowContext(ctx, `
		SELECT
			expires_at,
			available_at
		FROM perfeng_control.reconciliation_leases
		WHERE run_id = $1
	`, candidate.snapshot.ID).
		Scan(&expiresAt, &availableAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return run.Claim{}, false, storageError(err)
	}
	if err == nil &&
		(now.Before(expiresAt) ||
			(candidate.snapshot.State != run.StateCancelling && now.Before(availableAt))) {
		return run.Claim{}, false, nil
	}

	lease := run.Lease{
		RunID:     candidate.snapshot.ID,
		Principal: candidate.principal,
		WorkerID:  workerID,
		Token:     newClaimToken(),
		ExpiresAt: now.Add(ttl),
	}

	_, err = tx.ExecContext(
		ctx,
		`
			INSERT INTO perfeng_control.reconciliation_leases (
				run_id,
				worker_id,
				token,
				expires_at,
				available_at
			)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (run_id) DO UPDATE SET
				worker_id = EXCLUDED.worker_id,
				token = EXCLUDED.token,
				expires_at = EXCLUDED.expires_at,
				available_at = EXCLUDED.available_at
		`,
		lease.RunID,
		lease.WorkerID,
		lease.Token,
		lease.ExpiresAt,
		now,
	)
	if err != nil {
		return run.Claim{}, false, storageError(err)
	}

	return run.Claim{Lease: lease, Run: candidate.snapshot}, true, nil
}

func newClaimToken() string {
	var token [16]byte
	_, _ = rand.Read(token[:])

	return hex.EncodeToString(token[:])
}

// lockOwnedClaim locks the same Run row as ClaimRuns, Cancel and Advance. It
// validates ownership using the stored lease and database time while holding it.
func lockOwnedClaim(
	ctx context.Context,
	tx *sql.Tx,
	lease run.Lease,
) (run.Run, time.Time, error) {
	current, err := lockedRun(ctx, tx, lease.Principal, lease.RunID)
	if errors.Is(err, run.ErrNotFound) {
		return run.Run{}, time.Time{}, run.ErrLeaseLost
	}
	if err != nil {
		return run.Run{}, time.Time{}, err
	}

	var owner string
	var token string
	var expiresAt time.Time

	err = tx.QueryRowContext(ctx, `
		SELECT
			worker_id,
			token,
			expires_at
		FROM perfeng_control.reconciliation_leases
		WHERE run_id = $1
	`, lease.RunID).Scan(&owner, &token, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, time.Time{}, run.ErrLeaseLost
	}
	if err != nil {
		return run.Run{}, time.Time{}, storageError(err)
	}

	now, err := dbNow(ctx, tx)
	if err != nil {
		return run.Run{}, time.Time{}, err
	}
	if owner != lease.WorkerID ||
		token != lease.Token ||
		!now.Before(expiresAt) ||
		contract.Terminal(string(current.State)) {
		return run.Run{}, time.Time{}, run.ErrLeaseLost
	}

	return current, now, nil
}

// RenewClaim replaces the stored expiry using database time after locking and
// returns the latest Run snapshot observed under that lock.
func (r *Repository) RenewClaim(
	ctx context.Context,
	lease run.Lease,
	ttl time.Duration,
) (run.Claim, error) {
	if !lease.Valid() || !run.ValidLeaseTTL(ttl) {
		return run.Claim{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.Claim{}, err
	}
	defer tx.Rollback()

	current, now, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.Claim{}, err
	}

	lease.ExpiresAt = now.Add(ttl)
	_, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.reconciliation_leases
		SET expires_at = $1
		WHERE run_id = $2
	`, lease.ExpiresAt, lease.RunID)
	if err != nil {
		return run.Claim{}, storageError(err)
	}

	if err = tx.Commit(); err != nil {
		return run.Claim{}, storageError(err)
	}

	return run.Claim{Lease: lease, Run: current}, nil
}

// ReleaseClaim expires an owned lease at database time and sets its next
// availability. CANCELLING runs can bypass the availability delay.
func (r *Repository) ReleaseClaim(
	ctx context.Context,
	lease run.Lease,
	delay time.Duration,
) error {
	if !lease.Valid() || !run.ValidRetryDelay(delay) {
		return run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, now, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.reconciliation_leases
		SET
			expires_at = $1,
			available_at = $2
		WHERE run_id = $3
	`, now, now.Add(delay), lease.RunID)
	if err != nil {
		return storageError(err)
	}

	return storageError(tx.Commit())
}

// AdvanceClaim serializes a revision-checked Run transition with lease ownership.
// A terminal transition expires the lease in the same transaction.
func (r *Repository) AdvanceClaim(
	ctx context.Context,
	lease run.Lease,
	revision int64,
	change run.Change,
) (run.Run, error) {
	if !lease.Valid() {
		return run.Run{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()

	current, now, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.Run{}, err
	}

	next, err := current.Transition(revision, change, now)
	if err != nil {
		return run.Run{}, err
	}

	snapshot, err := json.Marshal(next)
	if err != nil {
		return run.Run{}, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.runs
		SET snapshot = $1
		WHERE run_id = $2
	`, snapshot, lease.RunID)
	if err != nil {
		return run.Run{}, storageError(err)
	}

	if contract.Terminal(string(next.State)) {
		_, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.reconciliation_leases
			SET expires_at = $1
			WHERE run_id = $2
		`, now, lease.RunID)
		if err != nil {
			return run.Run{}, storageError(err)
		}
	}

	if err = tx.Commit(); err != nil {
		return run.Run{}, storageError(err)
	}

	return next, nil
}
