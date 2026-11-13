package postgres

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// rerunGrant locks current grant rows in fixed permission order. Revocation and
// acceptance serialize on these rows; a previous observation is never a grant.
func rerunGrant(ctx context.Context, tx *sql.Tx, actor serviceaccess.Identity, sourceID string, write bool) (serviceaccess.Binding, error) {
	var b serviceaccess.Binding
	err := tx.QueryRowContext(ctx, `SELECT service_issuer,service_id,creator_issuer,creator_id,creator_kind,owner_principal,review_ref FROM perfeng_control.run_services WHERE run_id=$1`, sourceID).Scan(&b.Service.Issuer, &b.Service.ID, &b.Creator.Issuer, &b.Creator.ID, &b.CreatorKind, &b.OwnerPrincipal, &b.ReviewReference)
	if err != nil {
		return b, storageError(err)
	}
	permissions := []serviceaccess.Permission{serviceaccess.ReadRun}
	if write {
		permissions = append(permissions, serviceaccess.Rerun)
	}
	for _, permission := range permissions {
		var enabled bool
		err = tx.QueryRowContext(ctx, `SELECT enabled FROM perfeng_control.service_grants WHERE service_issuer=$1 AND service_id=$2 AND actor_issuer=$3 AND actor_id=$4 AND permission=$5 FOR SHARE`, b.Service.Issuer, b.Service.ID, actor.Issuer, actor.ID, permission).Scan(&enabled)
		if err != nil {
			return b, storageError(err)
		}
		if !enabled {
			return b, run.ErrNotFound
		}
	}
	return b, nil
}

type rerunDocument struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Acceptance    runrerun.Acceptance `json:"acceptance"`
}

func rerunRecord(ctx context.Context, tx *sql.Tx, actor serviceaccess.Identity, key string) (runrerun.Acceptance, error) {
	var value runrerun.Acceptance
	var payload, inputs, snapshot []byte
	var digest, inputDigest string
	var document rerunDocument
	err := tx.QueryRowContext(ctx, `SELECT i.receipt,i.sha256,c.payload,c.sha256,r.snapshot FROM perfeng_control.rerun_intents i JOIN perfeng_control.report_comparison_inputs c ON c.principal=i.source_owner AND c.run_id=i.source_run_id JOIN perfeng_control.runs r ON r.run_id=i.source_run_id WHERE i.actor_issuer=$1 AND i.actor_id=$2 AND i.idempotency_key=$3`, actor.Issuer, actor.ID, key).Scan(&payload, &digest, &inputs, &inputDigest, &snapshot)
	if err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	hash := sha256.Sum256(payload)
	if !jsondocument.Valid(payload, 65536) || hex.EncodeToString(hash[:]) != digest || !jsondocument.Decode(payload, &document) || document.SchemaVersion != 1 {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	value = document.Acceptance
	current, err := decodeRun(snapshot)
	if err != nil {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	value.Inputs, err = restoreReportInputs(inputs, inputDigest, current)
	if err != nil || value.Validate() != nil || value.Intent.Actor != actor || value.Intent.Key != key || value.Intent.SourceRunID != current.ID {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	return value, nil
}

// GetRerun recovers the originating actor's exact private acceptance with current
// read authority. Caller delegation and all evidence authorization remain inbound
// provider prerequisites; this method is not a public receipt encoder.
func (r *Repository) GetRerun(ctx context.Context, actor serviceaccess.Identity, sourceID, key string) (runrerun.Acceptance, error) {
	if !actor.Valid() || !run.ValidKey(key) {
		return runrerun.Acceptance{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	defer tx.Rollback()
	binding, err := rerunGrant(ctx, tx, actor, sourceID, false)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	value, err := rerunRecord(ctx, tx, actor, key)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	if value.Intent.SourceRunID != sourceID {
		return runrerun.Acceptance{}, run.ErrNotFound
	}
	if value.Source != binding {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	return value, nil
}

// AcceptRerun atomically stores child, service lineage and original acceptance.
// It is deliberately not wired into production: verified source/report admission
// and immutable execution fencing must qualify before any caller is enabled.
func (r *Repository) AcceptRerun(ctx context.Context, intent runrerun.Intent) (runrerun.Acceptance, error) {
	if err := intent.Validate(); err != nil {
		return runrerun.Acceptance{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	defer tx.Rollback()
	scope, _ := json.Marshal([]string{"rerunRun", intent.Actor.Issuer, intent.Actor.ID, intent.Key})
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, scopeLock("rerunRun", string(scope))); err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	binding, err := rerunGrant(ctx, tx, intent.Actor, intent.SourceRunID, true)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	previous, err := rerunRecord(ctx, tx, intent.Actor, intent.Key)
	if err == nil {
		if previous.Intent.SourceRunID != intent.SourceRunID {
			binding, err = rerunGrant(ctx, tx, intent.Actor, previous.Intent.SourceRunID, false)
			if err != nil {
				return runrerun.Acceptance{}, err
			}
		}
		if previous.Source != binding {
			return runrerun.Acceptance{}, run.ErrUnavailable
		}
		return previous.Replay(intent, now)
	}
	if !errors.Is(err, run.ErrNotFound) {
		return runrerun.Acceptance{}, err
	}
	value := runrerun.Acceptance{Intent: intent, Source: binding, AcceptedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	var inputs, snapshot []byte
	var digest string
	var target run.ExecutionTarget
	var report []byte
	err = tx.QueryRowContext(ctx, `SELECT i.payload,i.sha256,r.snapshot,r.execution_target,a.reference FROM perfeng_control.report_comparison_inputs i JOIN perfeng_control.runs r USING(principal,run_id) JOIN perfeng_control.artifacts a ON a.run_id=r.run_id AND a.artifact_id=$3 WHERE i.principal=$1 AND i.run_id=$2`, binding.OwnerPrincipal, intent.SourceRunID, intent.ReportID).Scan(&inputs, &digest, &snapshot, &target, &report)
	if err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	source, err := decodeRun(snapshot)
	if err != nil {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	value.Inputs, err = restoreReportInputs(inputs, digest, source)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	if !jsondocument.Decode(report, &value.Report) || value.Report.Validate() != nil || value.Report.Kind != "normalized" || value.Report.SizeBytes < 1 || value.Report.SizeBytes > 1048576 || value.Report.Format != "analysis-result/v1" || value.Report.MediaType != "application/json" || value.Report.SHA256 != intent.ReportSHA256 {
		return runrerun.Acceptance{}, run.ErrValidation
	}
	records := slices.Clone(value.Inputs.BaselineSnapshots)
	slices.SortFunc(records, func(a, b baseline.Record) int {
		if order := cmp.Compare(a.ID, b.ID); order != 0 {
			return order
		}
		return cmp.Compare(a.Version, b.Version)
	})
	for _, record := range records {
		frozen, _ := json.Marshal(record)
		var approved bool
		err = tx.QueryRowContext(ctx, `SELECT snapshot=$3::jsonb AND snapshot->>'state'='APPROVED' FROM perfeng_control.baselines WHERE principal=$1 AND baseline_id=$2 AND version=$4 FOR SHARE`, binding.OwnerPrincipal, record.ID, frozen, record.Version).Scan(&approved)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !approved) {
			return runrerun.Acceptance{}, run.ErrValidation
		}
		if err != nil {
			return runrerun.Acceptance{}, storageError(err)
		}
	}
	var token [16]byte
	_, _ = rand.Read(token[:])
	token[6] = (token[6] & 15) | 64
	token[8] = (token[8] & 63) | 128
	value.ID = fmt.Sprintf("%x-%x-%x-%x-%x", token[:4], token[4:6], token[6:8], token[8:10], token[10:])
	childID := "perf-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(token[:4])
	actor, _ := json.Marshal(intent.Actor)
	owner := sha256.Sum256(actor)
	value.ChildPrincipal = "rerun:" + hex.EncodeToString(owner[:])
	value.Child = run.Run{ID: childID, State: run.StateCreated, Revision: 1, Request: source.Request, CreatedAt: now, UpdatedAt: now}
	child, _ := json.Marshal(value.Child)
	if _, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.runs(run_id,principal,execution_target,snapshot) VALUES($1,$2,$3,$4)`, childID, value.ChildPrincipal, target, child); err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.run_services(run_id,owner_principal,service_issuer,service_id,creator_issuer,creator_id,creator_kind,review_ref) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, childID, value.ChildPrincipal, binding.Service.Issuer, binding.Service.ID, intent.Actor.Issuer, intent.Actor.ID, intent.ActorKind, "rerun/"+value.ID); err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	payload, err := json.Marshal(rerunDocument{SchemaVersion: 1, Acceptance: value})
	if err != nil || len(payload) > 65536 {
		return runrerun.Acceptance{}, run.ErrValidation
	}
	hash := sha256.Sum256(payload)
	_, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.rerun_intents(actor_issuer,actor_id,idempotency_key,source_owner,source_run_id,report_id,child_owner,child_run_id,receipt_id,accepted_at,expires_at,receipt,sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, intent.Actor.Issuer, intent.Actor.ID, intent.Key, binding.OwnerPrincipal, intent.SourceRunID, intent.ReportID, value.ChildPrincipal, childID, value.ID, now, value.ExpiresAt, payload, hex.EncodeToString(hash[:]))
	if err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	return value, nil
}
