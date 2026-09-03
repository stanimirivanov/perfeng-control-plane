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

// ClaimRuns uses short database transactions only. No external I/O belongs
// inside these locks. Empty/short batches are normal when other workers hold rows.
func (r *Repository) ClaimRuns(ctx context.Context, workerID string, limit int, ttl time.Duration) ([]run.Claim, error) {
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
	rows, err := tx.QueryContext(ctx, `
		SELECT r.principal,r.snapshot
		FROM perfeng_control.runs r
		LEFT JOIN perfeng_control.reconciliation_leases l ON l.run_id=r.run_id
		WHERE r.snapshot->>'state' IN
			('CREATED','VALIDATING','PROVISIONING','WARMING_UP','RUNNING',
			 'COLLECTING','ANALYZING','REPORTING','CANCELLING')
		AND (l.run_id IS NULL OR (
			l.expires_at <= clock_timestamp() AND
			(l.available_at <= clock_timestamp() OR r.snapshot->>'state'='CANCELLING')))
		ORDER BY (r.snapshot->>'state'='CANCELLING') DESC,
			l.available_at NULLS FIRST,r.run_id
		LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	candidates := make([]run.Claim, 0, limit)
	for rows.Next() {
		var principal string
		var b []byte
		if err = rows.Scan(&principal, &b); err != nil {
			break
		}
		current, decodeErr := decodeRun(b)
		if decodeErr != nil {
			err = decodeErr
			break
		}
		candidates = append(candidates, run.Claim{Lease: run.Lease{Principal: principal, RunID: current.ID, WorkerID: workerID}, Run: current})
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, storageError(rowErr)
	}
	result := make([]run.Claim, 0, len(candidates))
	for _, candidate := range candidates {
		// The initial JOIN can have an older READ COMMITTED snapshot even if its
		// run lock was acquired after another claimant committed a lease.
		// Re-read eligibility in a NEW statement while holding that run lock.
		now, err := dbNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		var expiry, available time.Time
		err = tx.QueryRowContext(ctx, "SELECT expires_at,available_at FROM perfeng_control.reconciliation_leases WHERE run_id=$1", candidate.Run.ID).Scan(&expiry, &available)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, storageError(err)
		}
		if err == nil && (now.Before(expiry) || (candidate.Run.State != "CANCELLING" && now.Before(available))) {
			continue
		}
		var token [16]byte
		_, _ = rand.Read(token[:])
		candidate.Lease.Token = hex.EncodeToString(token[:])
		candidate.Lease.ExpiresAt = now.Add(ttl)
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO perfeng_control.reconciliation_leases(run_id,worker_id,token,expires_at,available_at)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (run_id) DO UPDATE SET
			worker_id=EXCLUDED.worker_id,token=EXCLUDED.token,
			expires_at=EXCLUDED.expires_at,available_at=EXCLUDED.available_at`,
			candidate.Run.ID, workerID, candidate.Lease.Token, candidate.Lease.ExpiresAt, now); err != nil {
			return nil, storageError(err)
		}
		result = append(result, candidate)
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}

// ownedClaim locks the same Run row as ClaimRuns, Cancel and Advance. The lease
// check uses DB time after acquiring the row lock, never a caller's expiry field.
func ownedClaim(ctx context.Context, tx *sql.Tx, lease run.Lease) (run.Run, time.Time, error) {
	current, err := lockedRun(ctx, tx, lease.Principal, lease.RunID)
	if errors.Is(err, run.ErrNotFound) {
		return run.Run{}, time.Time{}, run.ErrLeaseLost
	}
	if err != nil {
		return run.Run{}, time.Time{}, err
	}
	var owner, token string
	var expiry time.Time
	err = tx.QueryRowContext(ctx, "SELECT worker_id,token,expires_at FROM perfeng_control.reconciliation_leases WHERE run_id=$1", lease.RunID).Scan(&owner, &token, &expiry)
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
	if owner != lease.WorkerID || token != lease.Token || !now.Before(expiry) || contract.Terminal(current.State) {
		return run.Run{}, time.Time{}, run.ErrLeaseLost
	}
	return current, now, nil
}

func (r *Repository) RenewClaim(ctx context.Context, lease run.Lease, ttl time.Duration) (run.Claim, error) {
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
	current, now, err := ownedClaim(ctx, tx, lease)
	if err != nil {
		return run.Claim{}, err
	}
	lease.ExpiresAt = now.Add(ttl)
	if _, err = tx.ExecContext(ctx, "UPDATE perfeng_control.reconciliation_leases SET expires_at=$1 WHERE run_id=$2", lease.ExpiresAt, lease.RunID); err != nil {
		return run.Claim{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return run.Claim{}, storageError(err)
	}
	return run.Claim{Lease: lease, Run: current}, nil
}

func (r *Repository) ReleaseClaim(ctx context.Context, lease run.Lease, delay time.Duration) error {
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
	_, now, err := ownedClaim(ctx, tx, lease)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE perfeng_control.reconciliation_leases SET expires_at=$1,available_at=$2 WHERE run_id=$3", now, now.Add(delay), lease.RunID); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit())
}

func (r *Repository) AdvanceClaim(ctx context.Context, lease run.Lease, revision int64, change run.Change) (run.Run, error) {
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
	current, now, err := ownedClaim(ctx, tx, lease)
	if err != nil {
		return run.Run{}, err
	}
	next, err := current.Transition(revision, change, now)
	if err != nil {
		return run.Run{}, err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return run.Run{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE perfeng_control.runs SET snapshot=$1 WHERE run_id=$2", b, lease.RunID); err != nil {
		return run.Run{}, storageError(err)
	}
	if contract.Terminal(next.State) {
		if _, err = tx.ExecContext(ctx, "UPDATE perfeng_control.reconciliation_leases SET expires_at=$1 WHERE run_id=$2", now, lease.RunID); err != nil {
			return run.Run{}, storageError(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return run.Run{}, storageError(err)
	}
	return next, nil
}
