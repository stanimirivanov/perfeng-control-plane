package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.PublicationRepository = (*Repository)(nil)

// RetainAnalysisPublication stores one complete verified proposal atomically.
// Exact retries skip verification (which may be nil for replay). New verification
// is in-process with no external I/O. Event then request locks preserve admission
// ordering; commit uncertainty requires identical retry or a scoped read.
func (r *Repository) RetainAnalysisPublication(ctx context.Context, principal string, message contract.AnalysisPublication, verify integration.PublicationVerifier) (payload []byte, err error) {
	raw, err := json.Marshal(message)
	if err != nil || !validPublicationPayload(raw, &message) {
		return nil, run.ErrValidation
	}
	if message.Data.Context.Principal.ID != principal {
		return nil, run.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && err == nil {
			payload, err = nil, storageError(rollbackErr)
		}
	}()
	identity := message.Data.Context.Request
	for _, scope := range [][]string{{"integration-publication-event", message.Source, message.ID}, {"integration-request", identity.Issuer, identity.ID}} {
		key, err := json.Marshal(scope)
		if err != nil {
			return nil, run.ErrValidation
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", scopeLock(principal, string(key))); err != nil {
			return nil, storageError(err)
		}
	}
	record, err := readIntegration(ctx, tx, principal, identity)
	if err != nil {
		return nil, err
	}
	previous, err := readAnalysisPublication(ctx, tx, principal, identity, record)
	if err == nil {
		if !bytes.Equal(previous, raw) {
			return nil, run.ErrConflict
		}
		return previous, nil
	}
	if !errors.Is(err, run.ErrNotFound) {
		return nil, err
	}
	if record.Native == nil {
		return nil, run.ErrConflict
	}
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM perfeng_control.integration_analysis_publications WHERE principal=$1 AND source=$2 AND event_id=$3)`, principal, message.Source, message.ID).Scan(&used); err != nil {
		return nil, storageError(err)
	}
	if used {
		return nil, run.ErrConflict
	}
	var reference []byte
	if err := tx.QueryRowContext(ctx, `SELECT reference FROM perfeng_control.artifacts WHERE run_id=$1 AND artifact_id::text=$2 FOR SHARE`, record.Run.ID, message.Data.Artifact.ID).Scan(&reference); err != nil {
		return nil, storageError(err)
	}
	var artifact run.Artifact
	if json.Unmarshal(reference, &artifact) != nil || !publicationMatches(message, record, artifact) {
		return nil, run.ErrConflict
	}
	if verify == nil {
		return nil, run.ErrValidation
	}
	// Give verification an independent proposal so it cannot alter retained bytes.
	var owned contract.AnalysisPublication
	if !jsondocument.Decode(raw, &owned) {
		return nil, run.ErrValidation
	}
	checked, verifyErr := verify(ctx, record, owned)
	if verifyErr != nil || !bytes.Equal(checked, raw) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, run.ErrValidation
	}
	digest := sha256.Sum256(raw)
	_, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.integration_analysis_publications(principal,issuer,request_id,source,event_id,artifact_id,payload,digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, principal, identity.Issuer, identity.ID, message.Source, message.ID, artifact.ID, raw, hex.EncodeToString(digest[:]))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return nil, run.ErrConflict
	}
	if err != nil {
		return nil, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}

	return append([]byte(nil), raw...), nil
}

// GetAnalysisPublication returns owned original bytes, verifying retained digest
// and parent/report association. It performs no current policy/approval lookup.
func (r *Repository) GetAnalysisPublication(ctx context.Context, principal string, identity contract.CatalogIdentity) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	record, err := readIntegration(ctx, r.db, principal, identity)
	if err != nil {
		return nil, err
	}

	return readAnalysisPublication(ctx, r.db, principal, identity, record)
}

func readAnalysisPublication(ctx context.Context, db integrationQuery, principal string, identity contract.CatalogIdentity, record integration.Record) ([]byte, error) {
	var raw, reference []byte
	var digest, source, event string
	err := db.QueryRowContext(ctx, `SELECT p.payload,p.digest,p.source,p.event_id,a.reference FROM perfeng_control.integration_analysis_publications p JOIN perfeng_control.artifacts a ON a.artifact_id=p.artifact_id WHERE p.principal=$1 AND p.issuer=$2 AND p.request_id=$3`, principal, identity.Issuer, identity.ID).Scan(&raw, &digest, &source, &event, &reference)
	if err != nil {
		return nil, storageError(err)
	}
	actual := sha256.Sum256(raw)
	var message contract.AnalysisPublication
	var artifact run.Artifact
	if hex.EncodeToString(actual[:]) != digest || !validPublicationPayload(raw, &message) || json.Unmarshal(reference, &artifact) != nil || message.Source != source || message.ID != event || !publicationMatches(message, record, artifact) {
		return nil, run.ErrUnavailable
	}

	return append([]byte(nil), raw...), nil
}

func validPublicationPayload(raw []byte, message *contract.AnalysisPublication) bool {
	return jsondocument.Valid(raw, contract.AnalysisPublicationMaximumBytes) && jsondocument.Decode(raw, message) && message.ValidContext() &&
		message.SpecVersion == "1.0" && message.Type == "dev.perfeng.integration.analysis-published.v1" && message.DataContentType == "application/json" &&
		message.DataSchema == "https://perfeng.dev/schemas/performance-analysis/v1/performance-analysis.schema.json" && message.ID != ""
}

func publicationMatches(message contract.AnalysisPublication, record integration.Record, artifact run.Artifact) bool {
	published, publishedErr := time.Parse(time.RFC3339Nano, message.Time)
	admitted, admittedErr := time.Parse(time.RFC3339Nano, record.Disposition.Time)
	if record.Native == nil || record.Run == nil || record.Disposition.Data.Run == nil ||
		publishedErr != nil || admittedErr != nil || published.Before(admitted) || message.Data.Artifact.Kind != "analysis" ||
		message.Source != record.Disposition.Source || !reflect.DeepEqual(message.Data.Context, record.Disposition.Data.Context) ||
		message.Data.Run != *record.Disposition.Data.Run || !reflect.DeepEqual(message.Data.Native, *record.Native) ||
		artifact.RunID != record.Run.ID || artifact.Kind != "normalized" || artifact.Format != "analysis-result/v1" || artifact.Validate() != nil {
		return false
	}
	var projected run.Artifact
	claim := message.Data.Artifact
	claim.Kind = "normalized"
	raw, err := json.Marshal(claim)

	return err == nil && json.Unmarshal(raw, &projected) == nil && projected == artifact
}
