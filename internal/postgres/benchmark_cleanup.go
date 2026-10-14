package postgres

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BeginBenchmarkCleanup seals dispatch before destructive operations and retains
// the first intended outcome. The confirmed quota fence starts the final drain clock.
func (r *Repository) BeginBenchmarkCleanup(
	ctx context.Context, lease run.Lease, revision int64, planSHA256 string, next run.Change,
) (run.BenchmarkReservation, error) {
	if !lease.Valid() || revision < 1 {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	defer tx.Rollback()

	current, now, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if current.Revision != revision {
		return run.BenchmarkReservation{}, run.ErrRevision
	}

	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !found || stored.Intent.PlanSHA256 != planSHA256 {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}

	if stored.Cleanup != nil {
		if !stored.Cleanup.ReleasedAt.IsZero() || (current.State != run.StateCancelling && !reflect.DeepEqual(stored.Cleanup.Next, next)) {
			return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
		}
	} else {
		if contract.Terminal(string(current.State)) {
			if next.State != current.State {
				return run.BenchmarkReservation{}, run.ErrTransition
			}
		} else if next.State != current.State {
			if _, err := current.Transition(revision, next, now); err != nil {
				return run.BenchmarkReservation{}, err
			}
		} else if current.State != run.StateCollecting && current.State != run.StateAnalyzing && current.State != run.StateReporting {
			return run.BenchmarkReservation{}, run.ErrTransition
		}

		encoded, err := json.Marshal(next)
		if err != nil {
			return run.BenchmarkReservation{}, run.ErrValidation
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.benchmark_target_reservations
			SET cleanup_started_at = $1, cleanup_not_before = $2, cleanup_change = $3
			WHERE run_id = $4
		`, now, now.Add(run.BenchmarkCleanupGrace), encoded, lease.RunID)
		if err != nil {
			return run.BenchmarkReservation{}, storageError(err)
		}
		stored.Cleanup = &run.BenchmarkCleanup{StartedAt: now, NotBefore: now.Add(run.BenchmarkCleanupGrace), Next: next}
	}

	if err := tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	return stored.Clone(), nil
}

// BindBenchmarkCleanupNamespace preserves the UID recovered by read-only namespace inspection.
func (r *Repository) BindBenchmarkCleanupNamespace(
	ctx context.Context, lease run.Lease, revision int64, planSHA256, uid string,
) (run.BenchmarkReservation, error) {
	if !lease.Valid() || revision < 1 || !run.ValidBenchmarkUID(uid) {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	defer tx.Rollback()

	current, _, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if current.Revision != revision {
		return run.BenchmarkReservation{}, run.ErrRevision
	}
	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !found || stored.Intent.PlanSHA256 != planSHA256 || stored.Cleanup == nil || !stored.Cleanup.ReleasedAt.IsZero() ||
		(stored.NamespaceUID != "" && stored.NamespaceUID != uid) {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.benchmark_target_reservations
		SET namespace_uid = $1 WHERE run_id = $2
	`, uid, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	if err := tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}
	stored.NamespaceUID = uid

	return stored, nil
}

// FinishBenchmarkCleanup atomically releases only verified, drained authority and
// applies cancellation priority. A terminal recovery changes no public Run fields.
func (r *Repository) FinishBenchmarkCleanup(
	ctx context.Context, lease run.Lease, revision int64, planSHA256 string, receipt []byte,
) (run.Run, error) {
	if !lease.Valid() || revision < 1 || len(receipt) < 1 || len(receipt) > 1<<20 {
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
	if current.Revision != revision {
		return run.Run{}, run.ErrRevision
	}
	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.Run{}, err
	}
	if !found || stored.Intent.PlanSHA256 != planSHA256 || stored.Cleanup == nil || stored.Cleanup.FencedAt.IsZero() ||
		!stored.Cleanup.ReleasedAt.IsZero() || now.Before(stored.Cleanup.NotBefore) {
		return run.Run{}, run.ErrBenchmarkConflict
	}

	var proof run.BenchmarkCleanupProof
	if !jsondocument.Valid(receipt, 1<<20) || !jsondocument.Decode(receipt, &proof) || !proof.Matches(stored) {
		return run.Run{}, run.ErrBenchmarkConflict
	}

	next := current
	if !contract.Terminal(string(current.State)) {
		change := stored.Cleanup.Next
		if current.State == run.StateCancelling {
			change = run.Change{State: run.StateAborted}
		}
		if change.State != current.State {
			next, err = current.Transition(revision, change, now)
			if err != nil {
				return run.Run{}, err
			}
		}
		encoded, err := json.Marshal(next)
		if err != nil {
			return run.Run{}, run.ErrValidation
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE perfeng_control.runs SET snapshot = $1 WHERE run_id = $2
		`, encoded, lease.RunID); err != nil {
			return run.Run{}, storageError(err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE perfeng_control.benchmark_target_reservations
		SET cleanup_receipt = $1, released_at = $2 WHERE run_id = $3
	`, receipt, now, lease.RunID); err != nil {
		return run.Run{}, storageError(err)
	}
	if contract.Terminal(string(next.State)) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE perfeng_control.reconciliation_leases SET expires_at = $1 WHERE run_id = $2
		`, now, lease.RunID); err != nil {
			return run.Run{}, storageError(err)
		}
	}

	if err := tx.Commit(); err != nil {
		return run.Run{}, storageError(err)
	}

	return next, nil
}

// RecordBenchmarkCleanupFence preserves the first confirmed quota barrier and
// starts its server-time drain budget once, including after a long worker outage.
func (r *Repository) RecordBenchmarkCleanupFence(
	ctx context.Context, lease run.Lease, revision int64, planSHA256 string,
) (run.BenchmarkReservation, error) {
	if !lease.Valid() || revision < 1 {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	defer tx.Rollback()

	current, now, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if current.Revision != revision {
		return run.BenchmarkReservation{}, run.ErrRevision
	}
	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !found || stored.Intent.PlanSHA256 != planSHA256 || stored.Cleanup == nil || !stored.Cleanup.ReleasedAt.IsZero() {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}
	if stored.Cleanup.FencedAt.IsZero() {
		_, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.benchmark_target_reservations
			SET cleanup_fenced_at = $1, cleanup_not_before = $2 WHERE run_id = $3
		`, now, now.Add(run.BenchmarkCleanupGrace), lease.RunID)
		if err != nil {
			return run.BenchmarkReservation{}, storageError(err)
		}
		stored.Cleanup.FencedAt = now
		stored.Cleanup.NotBefore = now.Add(run.BenchmarkCleanupGrace)
		stored.Cleanup.Ready = false
	}

	if err := tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	return stored, nil
}
