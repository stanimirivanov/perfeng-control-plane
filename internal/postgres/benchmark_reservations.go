package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ run.BenchmarkReservationStore = (*Repository)(nil)

// ReserveBenchmark commits an exclusive logical-target hold without making any external call.
// The Run lock precedes uniqueness contention; an ambiguous commit must be retried identically.
func (r *Repository) ReserveBenchmark(
	ctx context.Context, lease run.Lease, revision int64,
	intent run.BenchmarkIntent, admission []byte,
) (run.BenchmarkReservation, error) {
	if !lease.Valid() || revision < 1 || !intent.Valid() || len(admission) < 1 || len(admission) > 1<<20 {
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
	if err = benchmarkProvisioning(current, revision); err != nil {
		return run.BenchmarkReservation{}, err
	}

	var target string
	if err = tx.QueryRowContext(ctx, `SELECT execution_target FROM perfeng_control.runs WHERE run_id = $1`, lease.RunID).Scan(&target); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}
	if target != string(run.ExecutionTargetKubernetes) {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.benchmark_target_reservations
		    (run_id, target_id, cluster_uid, plan, plan_sha256, admission, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
	`, lease.RunID, intent.TargetID, intent.ClusterUID, intent.Plan, intent.PlanSHA256, admission, now)
	if err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !found {
		return run.BenchmarkReservation{}, run.ErrTargetReserved
	}
	if !stored.Intent.Same(intent) {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}

	if err = tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	return stored, nil
}

// GetBenchmarkReservation reads only the lease-owning Run's retained intent and materializations.
func (r *Repository) GetBenchmarkReservation(ctx context.Context, lease run.Lease) (run.BenchmarkReservation, bool, error) {
	if !lease.Valid() {
		return run.BenchmarkReservation{}, false, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return run.BenchmarkReservation{}, false, err
	}
	defer tx.Rollback()

	if _, _, err = lockOwnedClaim(ctx, tx, lease); err != nil {
		return run.BenchmarkReservation{}, false, err
	}
	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, false, err
	}

	if err = tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, false, storageError(err)
	}

	return stored, found, nil
}

// BindBenchmarkNamespace preserves the first observed UID after a possibly ambiguous namespace create.
func (r *Repository) BindBenchmarkNamespace(
	ctx context.Context, lease run.Lease, revision int64, planSHA256, uid string,
) (run.BenchmarkReservation, error) {
	if !run.ValidBenchmarkUID(uid) {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	return r.materializeBenchmark(ctx, lease, revision, planSHA256, uid, nil)
}

// SealBenchmarkRequest commits request bytes before ConfigMap, RBAC or Job creation can begin.
func (r *Repository) SealBenchmarkRequest(
	ctx context.Context, lease run.Lease, revision int64, planSHA256 string, request []byte,
) (run.BenchmarkReservation, error) {
	if len(request) < 1 || len(request) > 1<<20 {
		return run.BenchmarkReservation{}, run.ErrValidation
	}

	return r.materializeBenchmark(ctx, lease, revision, planSHA256, "", request)
}

// materializeBenchmark owns the Run-before-reservation lock order and write-once CAS checks.
func (r *Repository) materializeBenchmark(
	ctx context.Context, lease run.Lease, revision int64, planSHA256, uid string, request []byte,
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

	current, _, err := lockOwnedClaim(ctx, tx, lease)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if err = benchmarkProvisioning(current, revision); err != nil {
		return run.BenchmarkReservation{}, err
	}
	stored, found, err := readBenchmarkReservation(ctx, tx, lease.RunID)
	if err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !found || stored.Intent.PlanSHA256 != planSHA256 {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}

	if uid != "" {
		if stored.NamespaceUID != "" && stored.NamespaceUID != uid {
			return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.benchmark_target_reservations
			SET namespace_uid = $1
			WHERE run_id = $2
		`, uid, lease.RunID)
		stored.NamespaceUID = uid
	} else {
		if stored.NamespaceUID == "" || (len(stored.Request) != 0 && !bytes.Equal(stored.Request, request)) {
			return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE perfeng_control.benchmark_target_reservations
			SET request = $1
			WHERE run_id = $2
		`, request, lease.RunID)
		stored.Request = bytes.Clone(request)
	}
	if err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	if err = tx.Commit(); err != nil {
		return run.BenchmarkReservation{}, storageError(err)
	}

	return stored, nil
}

func benchmarkProvisioning(current run.Run, revision int64) error {
	if current.Revision != revision {
		return run.ErrRevision
	}
	if current.State != run.StateProvisioning {
		return run.ErrTransition
	}

	return nil
}

// readBenchmarkReservation requires the corresponding Run row lock; all writers use that fence.
func readBenchmarkReservation(ctx context.Context, tx *sql.Tx, runID string) (run.BenchmarkReservation, bool, error) {
	var stored run.BenchmarkReservation
	var uid sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT run_id, target_id, cluster_uid::text, plan, plan_sha256, admission, namespace_uid::text, request
		FROM perfeng_control.benchmark_target_reservations WHERE run_id = $1
	`, runID).Scan(&stored.RunID, &stored.Intent.TargetID, &stored.Intent.ClusterUID, &stored.Intent.Plan, &stored.Intent.PlanSHA256, &stored.Admission, &uid, &stored.Request)
	if errors.Is(err, sql.ErrNoRows) {
		return run.BenchmarkReservation{}, false, nil
	}
	if err != nil {
		return run.BenchmarkReservation{}, false, storageError(err)
	}
	stored.NamespaceUID = uid.String
	if !stored.Intent.Valid() || len(stored.Admission) == 0 || (uid.Valid && !run.ValidBenchmarkUID(uid.String)) {
		return run.BenchmarkReservation{}, false, errors.New("invalid stored benchmark reservation")
	}
	return stored, true, nil
}
