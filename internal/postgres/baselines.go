package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ baseline.Repository = (*Repository)(nil)

func decodeBaseline(data []byte) (baseline.Record, error) {
	var record baseline.Record
	if json.Unmarshal(data, &record) != nil || record.Validate() != nil {
		return baseline.Record{}, errors.New("invalid stored baseline snapshot")
	}

	return record, nil
}

// CreateBaseline uses database time and holds the source Run lock while it
// verifies the registered normalized artifact and inserts the candidate.
func (r *Repository) CreateBaseline(
	ctx context.Context,
	principal string,
	input baseline.Create,
) (baseline.Record, error) {
	if principal == "" {
		return baseline.Record{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return baseline.Record{}, err
	}
	defer tx.Rollback()

	now, err := dbNow(ctx, tx)
	if err != nil {
		return baseline.Record{}, err
	}
	record, err := baseline.New(input, now)
	if err != nil {
		return baseline.Record{}, err
	}
	if err = verifyBaselineEvidence(ctx, tx, principal, record); err != nil {
		return baseline.Record{}, err
	}

	snapshot, err := json.Marshal(record)
	if err != nil {
		return baseline.Record{}, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.baselines(
			principal,baseline_id,version,source_run_id,artifact_id,revision,state,snapshot
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (principal,baseline_id,version) DO NOTHING`,
		principal, record.ID, record.Version, record.SourceRunID, record.Artifact.ID,
		record.Revision, record.State, snapshot)
	if err != nil {
		return baseline.Record{}, storageError(err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return baseline.Record{}, storageError(err)
	}
	if inserted == 0 {
		return baseline.Record{}, run.ErrConflict
	}

	if err = tx.Commit(); err != nil {
		return baseline.Record{}, storageError(err)
	}

	return record.Clone(), nil
}

// verifyBaselineEvidence holds a share lock on the principal-owned source Run
// through the caller's transaction and compares the exact immutable artifact
// registry entry. It performs no external object-store reads.
func verifyBaselineEvidence(
	ctx context.Context,
	tx *sql.Tx,
	principal string,
	record baseline.Record,
) error {
	var runSnapshot []byte
	err := tx.QueryRowContext(ctx, `
		SELECT snapshot FROM perfeng_control.runs
		WHERE principal=$1 AND run_id=$2 FOR SHARE`,
		principal, record.SourceRunID).Scan(&runSnapshot)
	if err != nil {
		return storageError(err)
	}

	source, err := decodeRun(runSnapshot)
	if err != nil {
		return err
	}
	if source.State != run.StateCompleted {
		return run.ErrValidation
	}

	var artifactReference []byte
	err = tx.QueryRowContext(ctx, `
		SELECT a.reference
		FROM perfeng_control.artifacts a
		JOIN perfeng_control.runs source ON source.run_id=a.run_id
		WHERE source.principal=$1 AND a.run_id=$2 AND a.artifact_id=$3`,
		principal, record.SourceRunID, record.Artifact.ID).Scan(&artifactReference)
	if err != nil {
		return storageError(err)
	}

	var registered run.Artifact
	if json.Unmarshal(artifactReference, &registered) != nil ||
		registered.Validate() != nil || registered != record.Artifact {
		return run.ErrValidation
	}

	return nil
}

// GetBaseline reads and validates the stored snapshot without exposing whether
// another principal owns the requested identity.
func (r *Repository) GetBaseline(
	ctx context.Context,
	principal, id, version string,
) (baseline.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	var snapshot []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT snapshot FROM perfeng_control.baselines
		WHERE principal=$1 AND baseline_id=$2 AND version=$3`,
		principal, id, version).Scan(&snapshot)
	if err != nil {
		return baseline.Record{}, storageError(err)
	}

	return decodeBaseline(snapshot)
}

// lockedBaseline locks one principal-owned baseline version until tx ends and
// returns its validated current snapshot.
func lockedBaseline(
	ctx context.Context,
	tx *sql.Tx,
	principal, id, version string,
) (baseline.Record, error) {
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `
		SELECT snapshot FROM perfeng_control.baselines
		WHERE principal=$1 AND baseline_id=$2 AND version=$3
		FOR UPDATE`, principal, id, version).Scan(&snapshot)
	if err != nil {
		return baseline.Record{}, storageError(err)
	}

	return decodeBaseline(snapshot)
}

// TransitionBaseline locks the version, obtains database time after that lock,
// and commits the domain transition and indexed state in one transaction.
func (r *Repository) TransitionBaseline(
	ctx context.Context,
	principal, id, version string,
	expectedRevision int64,
	change baseline.Change,
) (baseline.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return baseline.Record{}, err
	}
	defer tx.Rollback()

	current, err := lockedBaseline(ctx, tx, principal, id, version)
	if err != nil {
		return baseline.Record{}, err
	}

	now, err := dbNow(ctx, tx)
	if err != nil {
		return baseline.Record{}, err
	}

	next, err := current.Transition(expectedRevision, change, now)
	if err != nil {
		return baseline.Record{}, err
	}

	snapshot, err := json.Marshal(next)
	if err != nil {
		return baseline.Record{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.baselines
		SET revision=$1,state=$2,snapshot=$3
		WHERE principal=$4 AND baseline_id=$5 AND version=$6`,
		next.Revision, next.State, snapshot, principal, id, version); err != nil {
		return baseline.Record{}, storageError(err)
	}

	if err = tx.Commit(); err != nil {
		return baseline.Record{}, storageError(err)
	}

	return next.Clone(), nil
}
