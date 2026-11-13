package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	batchv1 "k8s.io/api/batch/v1"
)

type rerunTemplateDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	Template      *batchv1.Job `json:"template"`
}

func decodeRerunTemplate(payload []byte, digest string) (*batchv1.Job, error) {
	sum := sha256.Sum256(payload)
	var document rerunTemplateDocument
	if !jsondocument.Valid(payload, 1<<20) || hex.EncodeToString(sum[:]) != digest || !jsondocument.Decode(payload, &document) || document.SchemaVersion != 1 || kubernetes.ValidateReusableJobTemplate(document.Template) != nil {
		return nil, run.ErrUnavailable
	}
	return document.Template.DeepCopy(), nil
}

// GetRerunTemplate reads the sealed reusable template for an exact child. It is
// private worker evidence, not dispatch authority; missing data never permits
// registry reconstruction for a known rerun after dispatch may have occurred.
func (r *Repository) GetRerunTemplate(ctx context.Context, principal string, current run.Run) (*batchv1.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if _, err := r.GetRerunExecution(ctx, principal, current); err != nil {
		return nil, err
	}
	var payload []byte
	var digest string
	err := r.db.QueryRowContext(ctx, `SELECT payload,sha256 FROM perfeng_control.rerun_templates WHERE child_owner=$1 AND child_run_id=$2`, principal, current.ID).Scan(&payload, &digest)
	if err != nil {
		return nil, storageError(err)
	}
	return decodeRerunTemplate(payload, digest)
}

// RetainRerunTemplate seals an independently reviewed reusable template during
// VALIDATING, before Kubernetes side effects. It proves no current service or
// registry admission and is deliberately not wired into runtime dispatch yet.
// Equal replay precedes lifecycle checks; different bytes cannot replace a seal.
func (r *Repository) RetainRerunTemplate(ctx context.Context, principal string, current run.Run, template *batchv1.Job) (*batchv1.Job, error) {
	return r.retainRerunTemplate(ctx, principal, current, template, nil)
}

// RetainClaimedRerunTemplate fences runtime capture/replay with current lease,
// revision, actor grants and exact baseline approval. Only VALIDATING can create
// a seal; PROVISIONING can replay identical evidence but cannot backfill it.
func (r *Repository) RetainClaimedRerunTemplate(ctx context.Context, claim run.Claim, template *batchv1.Job) (*batchv1.Job, error) {
	if !claim.Lease.Valid() || claim.Lease.RunID != claim.Run.ID {
		return nil, run.ErrValidation
	}
	return r.retainRerunTemplate(ctx, claim.Lease.Principal, claim.Run, template, &claim)
}
func (r *Repository) retainRerunTemplate(ctx context.Context, principal string, current run.Run, template *batchv1.Job, claim *run.Claim) (*batchv1.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	payload, err := json.Marshal(rerunTemplateDocument{SchemaVersion: 1, Template: template})
	if err != nil || len(payload) > 1<<20 {
		return nil, run.ErrValidation
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	owned, err := decodeRerunTemplate(payload, digest)
	if err != nil {
		return nil, run.ErrValidation
	}
	accepted, err := r.GetRerunExecution(ctx, principal, current)
	if err != nil {
		return nil, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var stored run.Run
	if claim != nil {
		stored, _, err = lockOwnedClaim(ctx, tx, claim.Lease)
	} else {
		stored, err = lockedRun(ctx, tx, principal, current.ID)
	}
	if err != nil {
		return nil, err
	}
	if stored.Request != current.Request || (claim != nil && stored.Revision != current.Revision) {
		return nil, run.ErrConflict
	}
	if claim != nil {
		if err = lockRerunValidationEligibility(ctx, tx, accepted); err != nil {
			return nil, err
		}
	}
	var previous []byte
	var previousDigest string
	err = tx.QueryRowContext(ctx, `SELECT payload,sha256 FROM perfeng_control.rerun_templates WHERE child_owner=$1 AND child_run_id=$2`, principal, current.ID).Scan(&previous, &previousDigest)
	if err == nil {
		old, decodeErr := decodeRerunTemplate(previous, previousDigest)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if !bytes.Equal(payload, previous) {
			return nil, run.ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError(err)
	}
	if stored.State != run.StateValidating {
		return nil, run.ErrConflict
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT execution_target='KUBERNETES' AND NOT EXISTS(SELECT 1 FROM perfeng_control.kubernetes_executions WHERE run_id=$1) FROM perfeng_control.runs WHERE run_id=$1`, current.ID).Scan(&eligible)
	if err != nil {
		return nil, storageError(err)
	}
	if !eligible {
		return nil, run.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.rerun_templates(child_owner,child_run_id,payload,sha256) VALUES($1,$2,$3,$4)`, principal, current.ID, payload, digest)
	if err != nil {
		return nil, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return owned, nil
}
