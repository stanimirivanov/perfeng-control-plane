package postgres

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ run.ArtifactRepository = (*Repository)(nil)

func (r *Repository) RegisterArtifact(ctx context.Context, principal string, artifact run.Artifact) error {
	artifact.ID = strings.ToLower(artifact.ID)
	if err := artifact.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = lockedRun(ctx, tx, principal, artifact.RunID); err != nil {
		return err
	}
	b, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.artifacts(artifact_id,run_id,reference)
		VALUES ($1,$2,$3) ON CONFLICT (artifact_id) DO NOTHING`, artifact.ID, artifact.RunID, b); err != nil {
		return storageError(err)
	}
	var stored []byte
	if err = tx.QueryRowContext(ctx, "SELECT reference FROM perfeng_control.artifacts WHERE artifact_id=$1", artifact.ID).Scan(&stored); err != nil {
		return storageError(err)
	}
	var previous run.Artifact
	if err = json.Unmarshal(stored, &previous); err != nil {
		return err
	}
	if previous != artifact {
		return run.ErrArtifactConflict
	}
	return storageError(tx.Commit())
}

func (r *Repository) GetArtifact(ctx context.Context, principal, runID, id string) (run.Artifact, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var b []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT a.reference FROM perfeng_control.artifacts a
		JOIN perfeng_control.runs r ON r.run_id=a.run_id
		WHERE r.principal=$1 AND r.run_id=$2 AND a.artifact_id::text=$3`, principal, runID, strings.ToLower(id)).Scan(&b)
	if err != nil {
		return run.Artifact{}, storageError(err)
	}
	var artifact run.Artifact
	if err = json.Unmarshal(b, &artifact); err != nil {
		return run.Artifact{}, err
	}
	return artifact, nil
}
