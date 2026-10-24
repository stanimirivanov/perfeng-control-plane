package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.RecoveryStore = (*Repository)(nil)

// CancelIntegration serializes delivery then request identity before locking the
// mapped Run. A concurrent terminal result wins; retries never create work.
func (r *Repository) CancelIntegration(ctx context.Context, principal string, message contract.IntegrationMessage) (integration.Record, error) {
	raw, err := json.Marshal(message)
	if err != nil || len(raw) > contract.IntegrationMaximumBytes || !message.ValidContext() ||
		message.Type != "dev.perfeng.integration.cancel-requested.v1" || message.Data.Context.Principal.ID != principal {
		return integration.Record{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return integration.Record{}, err
	}
	defer tx.Rollback()
	claims := message.Data.Context
	for _, scope := range [][]string{{"integration-event", message.Source, message.ID}, {"integration-request", claims.Request.Issuer, claims.Request.ID}} {
		key, _ := json.Marshal(scope)
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", scopeLock(principal, string(key))); err != nil {
			return integration.Record{}, storageError(err)
		}
	}
	digest := sha256.Sum256(raw)
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT digest FROM perfeng_control.integration_events WHERE principal=$1 AND source=$2 AND event_id=$3`, principal, message.Source, message.ID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return integration.Record{}, storageError(err)
	}
	if err == nil && previous != hex.EncodeToString(digest[:]) {
		return integration.Record{}, run.ErrConflict
	}
	result, err := readIntegration(ctx, tx, principal, claims.Request)
	if err != nil {
		return integration.Record{}, err
	}
	if !reflect.DeepEqual(result.Disposition.Data.Context, claims) || result.Run == nil {
		return integration.Record{}, run.ErrConflict
	}
	current, err := lockedRun(ctx, tx, principal, result.Run.ID)
	if err != nil {
		return integration.Record{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return integration.Record{}, err
	}
	next, err := current.RequestCancellation(now)
	if errors.Is(err, run.ErrTerminal) {
		next, err = current, nil
	}
	if err != nil {
		return integration.Record{}, err
	}
	if next.Revision != current.Revision {
		if err := updateNativeRun(ctx, tx, next); err != nil {
			return integration.Record{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO perfeng_control.integration_events(principal,source,event_id,digest,issuer,request_id)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(principal,source,event_id) DO NOTHING`, principal, message.Source, message.ID, hex.EncodeToString(digest[:]), claims.Request.Issuer, claims.Request.ID); err != nil {
		return integration.Record{}, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return integration.Record{}, storageError(err)
	}
	result.Run = &next
	return result, nil
}

// RecoverIntegration locks only overdue active Run rows, skipping contention.
// Native cancellation/lease reconciliation confirms external termination later.
func (r *Repository) RecoverIntegration(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.snapshot,(i.context->>'deadline')::timestamptz FROM perfeng_control.runs r
		JOIN perfeng_control.integration_requests i ON i.principal=r.principal AND i.run_id=r.run_id
		WHERE (i.context->>'deadline')::timestamptz <= clock_timestamp()
		AND r.snapshot->>'state' IN ('CREATED','VALIDATING','PROVISIONING','WARMING_UP','RUNNING','COLLECTING','ANALYZING','REPORTING')
		ORDER BY (i.context->>'deadline')::timestamptz,r.run_id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, limit)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	type candidate struct {
		current  run.Run
		deadline time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var raw []byte
		var deadline time.Time
		if err := rows.Scan(&raw, &deadline); err != nil {
			return storageError(err)
		}
		current, err := decodeRun(raw)
		if err != nil {
			return err
		}
		candidates = append(candidates, candidate{current, deadline})
	}
	if err := rows.Err(); err != nil {
		return storageError(err)
	}
	if err := rows.Close(); err != nil {
		return storageError(err)
	}
	for _, candidate := range candidates {
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		if now.Before(candidate.deadline) {
			continue
		}
		next, err := candidate.current.RequestCancellation(now)
		if err != nil {
			return err
		}
		if err := updateNativeRun(ctx, tx, next); err != nil {
			return err
		}
	}
	return storageError(tx.Commit())
}
