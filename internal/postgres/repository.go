package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func decodeRun(b []byte) (run.Run, error) {
	var result run.Run
	if err := json.Unmarshal(b, &result); err != nil {
		return run.Run{}, errors.New("invalid stored run snapshot")
	}
	return result, nil
}

func dbNow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now)
	return now.UTC(), storageError(err)
}

// Scope encoding is unambiguous. A 64-bit advisory-hash collision merely
// serializes unrelated keys; the full principal/key primary key decides identity.
func scopeLock(principal, key string) int64 {
	b, _ := json.Marshal([]string{"createRun", principal, key})
	hash := sha256.Sum256(b)
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func (r *Repository) Accept(ctx context.Context, principal, key string, request run.Request) (run.Accepted, error) {
	if principal == "" || !run.ValidKey(key) || request.Validate() != nil {
		return run.Accepted{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return run.Accepted{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", scopeLock(principal, key)); err != nil {
		return run.Accepted{}, storageError(err)
	}
	// Read the DB clock AFTER acquiring the lock, not transaction-start time.
	now, err := dbNow(ctx, tx)
	if err != nil {
		return run.Accepted{}, err
	}
	var original []byte
	var expiry time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT original_snapshot,expires_at FROM perfeng_control.create_bindings
		WHERE principal=$1 AND idempotency_key=$2`, principal, key).Scan(&original, &expiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return run.Accepted{}, storageError(err)
	}
	if err == nil && now.Before(expiry) {
		previous, err := decodeRun(original)
		if err != nil {
			return run.Accepted{}, err
		}
		if previous.Request != request {
			return run.Accepted{}, run.ErrConflict
		}
		// No mutation. Rollback releases the lock without touching the binding.
		return run.Accepted{Run: previous, ExpiresAt: expiry.UTC()}, nil
	}
	var suffix [4]byte
	_, _ = rand.Read(suffix[:])
	id := "perf-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:])
	created := run.Run{ID: id, State: "CREATED", Revision: 1, Request: request, CreatedAt: now, UpdatedAt: now}
	snapshot, err := json.Marshal(created)
	if err != nil {
		return run.Accepted{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO perfeng_control.runs(run_id,principal,snapshot) VALUES ($1,$2,$3)", id, principal, snapshot); err != nil {
		// An extremely rare ID collision rolls back with no key reservation.
		return run.Accepted{}, storageError(err)
	}
	expiry = now.Add(24 * time.Hour)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.create_bindings(principal,idempotency_key,run_id,original_snapshot,expires_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (principal,idempotency_key) DO UPDATE SET
		run_id=EXCLUDED.run_id, original_snapshot=EXCLUDED.original_snapshot, expires_at=EXCLUDED.expires_at`,
		principal, key, id, snapshot, expiry); err != nil {
		return run.Accepted{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return run.Accepted{}, storageError(err)
	}
	return run.Accepted{Run: created, ExpiresAt: expiry}, nil
}

func (r *Repository) Get(ctx context.Context, principal, id string) (run.Run, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var snapshot []byte
	err := r.db.QueryRowContext(ctx, "SELECT snapshot FROM perfeng_control.runs WHERE principal=$1 AND run_id=$2", principal, id).Scan(&snapshot)
	if err != nil {
		return run.Run{}, storageError(err)
	}
	return decodeRun(snapshot)
}

func lockedRun(ctx context.Context, tx *sql.Tx, principal, id string) (run.Run, error) {
	var snapshot []byte
	err := tx.QueryRowContext(ctx, "SELECT snapshot FROM perfeng_control.runs WHERE principal=$1 AND run_id=$2 FOR UPDATE", principal, id).Scan(&snapshot)
	if err != nil {
		return run.Run{}, storageError(err)
	}
	return decodeRun(snapshot)
}

func (r *Repository) mutate(ctx context.Context, principal, id string, change func(run.Run, time.Time) (run.Run, error)) (run.Run, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	current, err := lockedRun(ctx, tx, principal, id)
	if err != nil {
		return run.Run{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return run.Run{}, err
	}
	next, err := change(current, now)
	if err != nil {
		return run.Run{}, err
	}
	if next.Revision == current.Revision {
		return current, nil
	}
	b, err := json.Marshal(next)
	if err != nil {
		return run.Run{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE perfeng_control.runs SET snapshot=$1 WHERE principal=$2 AND run_id=$3", b, principal, id); err != nil {
		return run.Run{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return run.Run{}, storageError(err)
	}
	return next, nil
}

func (r *Repository) Cancel(ctx context.Context, principal, id string) (run.Run, error) {
	return r.mutate(ctx, principal, id, func(current run.Run, now time.Time) (run.Run, error) {
		if current.State == "CANCELLING" || current.State == "ABORTED" {
			return current, nil
		}
		if contract.Terminal(current.State) {
			return run.Run{}, run.ErrTerminal
		}
		return current.Transition(current.Revision, run.Change{State: "CANCELLING"}, now)
	})
}

func (r *Repository) Advance(ctx context.Context, principal, id string, revision int64, change run.Change) (run.Run, error) {
	return r.mutate(ctx, principal, id, func(current run.Run, now time.Time) (run.Run, error) {
		return current.Transition(revision, change, now)
	})
}
