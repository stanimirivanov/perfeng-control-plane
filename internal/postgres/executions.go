package postgres

import (
	"context"
	"database/sql"
	"errors"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ kubernetes.ExecutionStore = (*Repository)(nil)

// BindExecution records the first Job identity for a Run. Identical retries are
// no-ops; a different identity cannot replace it.
func (r *Repository) BindExecution(
	ctx context.Context,
	lease run.Lease,
	execution kubernetes.Execution,
) error {
	if !lease.Valid() || !execution.Valid() || execution.RunID != lease.RunID {
		return run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, _, err = lockOwnedClaim(ctx, tx, lease); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.kubernetes_executions (
			run_id,
			namespace,
			job_name,
			job_uid,
			spec_sha256
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (run_id) DO NOTHING
	`, execution.RunID, execution.Namespace, execution.JobName, string(execution.UID), execution.SpecSHA256)
	if err != nil {
		return storageError(err)
	}

	stored, found, err := getExecution(ctx, tx, execution.RunID)
	if err != nil {
		return err
	}
	if !found || stored != execution {
		return kubernetes.ErrExecutionConflict
	}

	return storageError(tx.Commit())
}

// GetExecution returns the immutable Job identity while the caller still owns
// the Run's reconciliation lease.
func (r *Repository) GetExecution(
	ctx context.Context,
	lease run.Lease,
) (kubernetes.Execution, bool, error) {
	if !lease.Valid() {
		return kubernetes.Execution{}, false, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return kubernetes.Execution{}, false, err
	}
	defer tx.Rollback()

	if _, _, err = lockOwnedClaim(ctx, tx, lease); err != nil {
		return kubernetes.Execution{}, false, err
	}
	execution, found, err := getExecution(ctx, tx, lease.RunID)
	if err != nil {
		return kubernetes.Execution{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return kubernetes.Execution{}, false, storageError(err)
	}

	return execution, found, nil
}

func getExecution(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (kubernetes.Execution, bool, error) {
	var execution kubernetes.Execution
	var uid string
	err := tx.QueryRowContext(ctx, `
		SELECT
			run_id,
			namespace,
			job_name,
			job_uid,
			spec_sha256
		FROM perfeng_control.kubernetes_executions
		WHERE run_id = $1
	`, runID).Scan(
		&execution.RunID,
		&execution.Namespace,
		&execution.JobName,
		&uid,
		&execution.SpecSHA256,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return kubernetes.Execution{}, false, nil
	}
	if err != nil {
		return kubernetes.Execution{}, false, storageError(err)
	}
	execution.UID = types.UID(uid)
	if !execution.Valid() {
		return kubernetes.Execution{}, false, errors.New("invalid stored Kubernetes execution")
	}

	return execution, true, nil
}
