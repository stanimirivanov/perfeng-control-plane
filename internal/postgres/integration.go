package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.Repository = (*Repository)(nil)

type integrationQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// GetIntegration reads one immutable disposition and its current owned Run in a
// single statement. No request identity is looked up outside principal scope.
func (r *Repository) GetIntegration(ctx context.Context, principal string, identity contract.CatalogIdentity) (integration.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return readIntegration(ctx, r.db, principal, identity)
}

func readIntegration(ctx context.Context, db integrationQuery, principal string, identity contract.CatalogIdentity) (integration.Record, error) {
	var disposition, snapshot, native, verification []byte
	err := db.QueryRowContext(ctx, `SELECT i.disposition,r.snapshot,i.native_binding,i.analysis_verification FROM perfeng_control.integration_requests i
		LEFT JOIN perfeng_control.runs r ON r.principal=i.principal AND r.run_id=i.run_id
		WHERE i.principal=$1 AND i.issuer=$2 AND i.request_id=$3`, principal, identity.Issuer, identity.ID).Scan(&disposition, &snapshot, &native, &verification)
	if err != nil {
		return integration.Record{}, storageError(err)
	}
	var result integration.Record
	if json.Unmarshal(disposition, &result.Disposition) != nil {
		return integration.Record{}, run.ErrUnavailable
	}
	if len(snapshot) != 0 {
		current, err := decodeRun(snapshot)
		if err != nil {
			return integration.Record{}, run.ErrUnavailable
		}
		result.Run = &current
	}
	result.Native, err = restoreIntegrationNative(native, result)
	if err != nil {
		return integration.Record{}, err
	}
	result.Verification, err = restoreIntegrationVerification(verification, result)
	if err != nil {
		return integration.Record{}, err
	}

	return result, nil
}

// AdmitIntegration serializes event then request identity, replays retained
// dispositions before new approval/deadline checks, and commits any new Run with
// its disposition and delivery digest. Review is in-process and must honor the
// integration.Review no-external-I/O contract. Native create bindings are untouched.
func (r *Repository) AdmitIntegration(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review integration.Review) (integration.Record, error) {
	return r.admitIntegration(ctx, principal, provider, message, review, nil)
}

func (r *Repository) admitIntegration(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review integration.Review,
	capture func(integration.Record) (contract.AnalysisNativeBinding, *integration.AnalysisVerification, error),
) (integration.Record, error) {
	raw, err := json.Marshal(message)
	if err != nil || len(raw) > contract.IntegrationMaximumBytes || review == nil ||
		message.Type != "dev.perfeng.integration.requested.v1" || !message.ValidContext() ||
		message.Source != message.Data.Context.Request.Issuer || message.Data.Context.Principal.ID != principal {
		return integration.Record{}, run.ErrValidation
	}
	for _, value := range []string{principal, provider, message.Source, message.ID, message.Data.Context.Request.ID, message.Data.Context.Attempt.Issuer, message.Data.Context.Attempt.ID} {
		if value == "" || strings.ContainsRune(value, 0) {
			return integration.Record{}, run.ErrValidation
		}
	}
	// Detach context pointers/slices from the caller before review or persistence.
	var owned contract.IntegrationMessage
	if json.Unmarshal(raw, &owned) != nil {
		return integration.Record{}, run.ErrValidation
	}
	message = owned
	claims := message.Data.Context
	digest := sha256.Sum256(raw)
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return integration.Record{}, err
	}
	defer tx.Rollback()
	for _, scope := range [][]string{{"integration-event", message.Source, message.ID}, {"integration-request", claims.Request.Issuer, claims.Request.ID}} {
		key, _ := json.Marshal(scope)
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", scopeLock(principal, string(key))); err != nil {
			return integration.Record{}, storageError(err)
		}
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT digest FROM perfeng_control.integration_events WHERE principal=$1 AND source=$2 AND event_id=$3`, principal, message.Source, message.ID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return integration.Record{}, storageError(err)
	}
	if err == nil && previous != hex.EncodeToString(digest[:]) {
		return integration.Record{}, run.ErrConflict
	}
	result, err := readIntegration(ctx, tx, principal, claims.Request)
	if err != nil && !errors.Is(err, run.ErrNotFound) {
		return integration.Record{}, err
	}
	if err == nil && !reflect.DeepEqual(result.Disposition.Data.Context, claims) {
		return integration.Record{}, run.ErrConflict
	}
	if errors.Is(err, run.ErrNotFound) {
		result, err = newIntegration(ctx, tx, principal, provider, message, review)
		if err != nil {
			return integration.Record{}, err
		}
		contextBytes, _ := json.Marshal(claims)
		responseBytes, _ := json.Marshal(result.Disposition)
		var runID any
		if result.Run != nil {
			runID = result.Run.ID
		}
		var native, verification any
		if capture != nil && result.Run != nil {
			binding, evidence, err := capture(result)
			if err != nil {
				return integration.Record{}, err
			}
			encoded, err := json.Marshal(binding)
			if err != nil {
				return integration.Record{}, run.ErrUnavailable
			}
			if _, err := restoreIntegrationNative(encoded, result); err != nil {
				return integration.Record{}, err
			}
			result.Native, native = &binding, encoded
			if evidence != nil {
				encoded, err := json.Marshal(verificationDocument{PolicyBytes: evidence.PolicyBytes, Producer: evidence.Producer, ContractsVersion: evidence.ContractsVersion})
				if err != nil {
					return integration.Record{}, run.ErrUnavailable
				}
				result.Verification, err = restoreIntegrationVerification(encoded, result)
				if err != nil {
					return integration.Record{}, err
				}
				verification = encoded
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.integration_requests(principal,issuer,request_id,attempt_issuer,attempt_id,context,disposition,run_id,native_binding,analysis_verification)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, principal, claims.Request.Issuer, claims.Request.ID, claims.Attempt.Issuer, claims.Attempt.ID, contextBytes, responseBytes, runID, native, verification)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return integration.Record{}, run.ErrConflict
		}
		if err != nil {
			return integration.Record{}, storageError(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.integration_events(principal,source,event_id,digest,issuer,request_id)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(principal,source,event_id) DO NOTHING`, principal, message.Source, message.ID, hex.EncodeToString(digest[:]), claims.Request.Issuer, claims.Request.ID); err != nil {
		return integration.Record{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return integration.Record{}, storageError(err)
	}
	return result, nil
}

func newIntegration(ctx context.Context, tx *sql.Tx, principal, provider string, message contract.IntegrationMessage, review integration.Review) (integration.Record, error) {
	now, err := dbNow(ctx, tx)
	if err != nil {
		return integration.Record{}, err
	}
	requested, _ := time.Parse(time.RFC3339Nano, message.Data.Context.RequestedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, message.Data.Context.Deadline)
	if now.Before(requested) {
		return integration.Record{}, run.ErrValidation
	}
	code := "expired"
	var request run.Request
	var target run.ExecutionTarget
	if now.Before(deadline) {
		// Review receives its own context copy and only a principal-scoped locked reader.
		encoded, _ := json.Marshal(message.Data.Context)
		var claims contract.IntegrationContext
		_ = json.Unmarshal(encoded, &claims)
		request, target, err = review(ctx, principal, claims, func(readCtx context.Context, owner, id, version string) (baseline.Record, error) {
			if owner != principal {
				return baseline.Record{}, run.ErrForbidden
			}
			var snapshot []byte
			if err := tx.QueryRowContext(readCtx, `SELECT snapshot FROM perfeng_control.baselines WHERE principal=$1 AND baseline_id=$2 AND version=$3 FOR SHARE`, owner, id, version).Scan(&snapshot); err != nil {
				return baseline.Record{}, storageError(err)
			}
			return decodeBaseline(snapshot)
		})
		if err != nil && !errors.Is(err, run.ErrForbidden) && !errors.Is(err, run.ErrValidation) && !errors.Is(err, integration.ErrUnsupportedRevision) {
			return integration.Record{}, storageError(err)
		}
		code = ""
		if err != nil {
			code = "unavailable_reference"
		}
		if errors.Is(err, integration.ErrUnsupportedRevision) {
			code = "unsupported_revision"
		}
		now, err = dbNow(ctx, tx)
		if err != nil {
			return integration.Record{}, err
		}
		if !now.Before(deadline) {
			code = "expired"
		}
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return integration.Record{}, run.ErrUnavailable
	}
	response := contract.IntegrationMessage{SpecVersion: "1.0", ID: hex.EncodeToString(token[:]), Source: provider, Type: "dev.perfeng.integration.rejected.v1", Time: now.Format(time.RFC3339Nano), DataContentType: "application/json", DataSchema: message.DataSchema, Data: contract.IntegrationData{Context: message.Data.Context}}
	if !response.ValidContext() {
		return integration.Record{}, run.ErrUnavailable
	}
	result := integration.Record{Disposition: response}
	if code != "" {
		result.Disposition.Data.Error = &contract.IntegrationError{Code: code, Message: "Request cannot be admitted"}
		return result, nil
	}
	if request.Validate() != nil || !target.Valid() {
		return integration.Record{}, run.ErrUnavailable
	}
	id := "perf-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(token[:4])
	created := run.Run{ID: id, State: run.StateCreated, Revision: 1, Request: request, CreatedAt: now, UpdatedAt: now}
	snapshot, err := json.Marshal(created)
	if err != nil {
		return integration.Record{}, run.ErrUnavailable
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO perfeng_control.runs(run_id,principal,execution_target,snapshot) SELECT $1,$2,$3,$4 WHERE clock_timestamp() < $5`, id, principal, target, snapshot, deadline)
	if err != nil {
		return integration.Record{}, storageError(err)
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return integration.Record{}, storageError(err)
	}
	if count == 0 {
		result.Disposition.Data.Error = &contract.IntegrationError{Code: "expired", Message: "Request cannot be admitted"}
		return result, nil
	}
	result.Run = &created
	result.Disposition.Type = "dev.perfeng.integration.accepted.v1"
	result.Disposition.Data.Run = &contract.CatalogIdentity{Issuer: provider, ID: id}
	return result, nil
}
