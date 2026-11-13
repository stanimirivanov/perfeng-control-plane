package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
)

var _ runcomparison.Repository = (*Repository)(nil)

// OpenWithReportCapture enrolls only newly admitted Runs; the option is immutable
// after construction. Existing Runs and idempotency replays are never backfilled.
func OpenWithReportCapture(ctx context.Context, dsn string) (*Repository, error) {
	repository, err := Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	repository.reportCapture = true
	return repository, nil
}

// comparisonInputsDocument versions private persistence independently of public APIs.
type comparisonInputsDocument struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Inputs        runcomparison.Inputs `json:"inputs"`
}

func restoreReportInputs(payload []byte, digest string, current run.Run) (runcomparison.Inputs, error) {
	sum := sha256.Sum256(payload)
	var document comparisonInputsDocument
	if !jsondocument.Valid(payload, 2<<20) || hex.EncodeToString(sum[:]) != digest || !jsondocument.Decode(payload, &document) || document.SchemaVersion != 1 || document.Inputs.Validate(current) != nil {
		return runcomparison.Inputs{}, run.ErrUnavailable
	}
	return document.Inputs.Clone(), nil
}

// GetReportInputs reads retained selections without live registry or approval checks.
// Missing/unmapped and cross-principal history is ErrNotFound, never reconstructed.
func (r *Repository) GetReportInputs(ctx context.Context, principal, id string) (runcomparison.Inputs, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var payload, snapshot []byte
	var digest string
	err := r.db.QueryRowContext(ctx, `SELECT i.payload,i.sha256,r.snapshot FROM perfeng_control.report_comparison_inputs i
 JOIN perfeng_control.runs r USING(principal,run_id) WHERE i.principal=$1 AND i.run_id=$2`, principal, id).Scan(&payload, &digest, &snapshot)
	if err != nil {
		return runcomparison.Inputs{}, storageError(err)
	}
	current, err := decodeRun(snapshot)
	if err != nil {
		return runcomparison.Inputs{}, run.ErrUnavailable
	}
	return restoreReportInputs(payload, digest, current)
}

// RetainReportInputs serializes on the native Run, replaying before current-state
// checks. The SQL guard locks exact baseline snapshots and rechecks approval.
// No registry, Kubernetes or object-store call occurs in this transaction.
func (r *Repository) RetainReportInputs(ctx context.Context, principal string, current run.Run, inputs runcomparison.Inputs) (runcomparison.Inputs, error) {
	inputs = inputs.Clone()
	if principal == "" || inputs.Validate(current) != nil {
		return runcomparison.Inputs{}, run.ErrValidation
	}
	payload, err := json.Marshal(comparisonInputsDocument{SchemaVersion: 1, Inputs: inputs})
	if err != nil || len(payload) > 2<<20 {
		return runcomparison.Inputs{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return runcomparison.Inputs{}, err
	}
	defer tx.Rollback()
	storedRun, err := lockedRun(ctx, tx, principal, current.ID)
	if err != nil {
		return runcomparison.Inputs{}, err
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM perfeng_control.report_comparison_inputs WHERE principal=$1 AND run_id=$2`, principal, current.ID).Scan(&previous)
	if err == nil {
		if !bytes.Equal(payload, previous) {
			return runcomparison.Inputs{}, run.ErrConflict
		}
		return inputs.Clone(), storageError(tx.Commit())
	}
	if !errors.Is(storageError(err), run.ErrNotFound) {
		return runcomparison.Inputs{}, storageError(err)
	}
	var enrolled bool
	if err := tx.QueryRowContext(ctx, `SELECT report_capture_enrolled FROM perfeng_control.runs WHERE run_id=$1`, current.ID).Scan(&enrolled); err != nil {
		return runcomparison.Inputs{}, storageError(err)
	}
	if !enrolled {
		return runcomparison.Inputs{}, runcomparison.ErrNotEnrolled
	}
	if storedRun.State != run.StateReporting || storedRun.Request != inputs.Request {
		return runcomparison.Inputs{}, run.ErrConflict
	}
	var reported bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM perfeng_control.artifacts WHERE run_id=$1 AND reference->>'format'='analysis-result/v1')`, current.ID).Scan(&reported); err != nil {
		return runcomparison.Inputs{}, storageError(err)
	}
	if reported {
		return runcomparison.Inputs{}, run.ErrConflict
	}
	sum := sha256.Sum256(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO perfeng_control.report_comparison_inputs(principal,run_id,candidate_id,payload,sha256) VALUES($1,$2,$3,$4,$5)`, principal, current.ID, inputs.Candidate.ID, payload, hex.EncodeToString(sum[:])); err != nil {
		return runcomparison.Inputs{}, storageError(err)
	}
	return inputs.Clone(), storageError(tx.Commit())
}
