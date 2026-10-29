package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func verificationFixture(t *testing.T) (integration.AnalysisVerification, run.Request) {
	t.Helper()
	request := testRequest(t)
	raw, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	request.Policy = run.Reference{ID: document.Metadata.Name, Version: document.Metadata.Version, SHA256: hex.EncodeToString(digest[:])}

	return integration.AnalysisVerification{PolicyBytes: raw, ContractsVersion: "0.25.0",
		Producer: rawresult.Producer{Name: "perfeng-analysis", Version: "1.0.0", Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("f", 64)}}, request
}

func TestIntegrationVerificationOfflineRestore(t *testing.T) {
	evidence, request := verificationFixture(t)
	record := integration.Record{Run: &run.Run{Request: request}, Native: &contract.AnalysisNativeBinding{}}
	raw, err := json.Marshal(verificationDocument{PolicyBytes: evidence.PolicyBytes, Producer: evidence.Producer, ContractsVersion: evidence.ContractsVersion})
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoreIntegrationVerification(raw, record)
	if err != nil || !reflect.DeepEqual(got, &evidence) {
		t.Fatal("exact historical inputs lost", err)
	}
	got.PolicyBytes[0] = '!'
	for _, bad := range [][]byte{[]byte(`{}`), []byte(`null`), append(raw, raw...), []byte(strings.Replace(string(raw), "0.25.0", "registry", 1))} {
		if got, err := restoreIntegrationVerification(bad, record); !errors.Is(err, run.ErrUnavailable) || got != nil {
			t.Fatal("malformed historical evidence accepted", err)
		}
	}
	record.Run.Request.Policy.SHA256 = strings.Repeat("0", 64)
	if got, err := restoreIntegrationVerification(raw, record); !errors.Is(err, run.ErrUnavailable) || got != nil {
		t.Fatal("wrong native pin accepted", err)
	}
	if got, err := restoreIntegrationVerification(nil, record); err != nil || got != nil {
		t.Fatal("legacy absence reconstructed", err)
	}
}

// Seed the old writer's columns directly: current repository reads require 0022.
// Also used by publication's populated pre-0021 upgrade test.
func seedPreVerificationAdmission(t *testing.T, r *Repository, suffix string) integration.Record {
	t.Helper()
	current := accepted(t, r, "alice", "verification-seed-"+suffix)
	message := integrationRequest(t, r, suffix)
	message.Data.Context.Baseline = nil
	message.Source, message.Type, message.Time = "https://provider.example/identity", "dev.perfeng.integration.accepted.v1", current.Run.CreatedAt.Format(time.RFC3339Nano)
	message.Data.Run = &contract.CatalogIdentity{Issuer: message.Source, ID: current.Run.ID}
	record := integration.Record{Run: &current.Run, Disposition: message}
	binding, err := freezeIntegrationNative(record, integration.AdmissionResolution{Request: current.Run.Request, Target: run.ExecutionTargetKubernetes,
		Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	record.Native = &binding
	documents := make([][]byte, 3)
	for i, value := range []any{message.Data.Context, message, binding} {
		documents[i], err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	claims := message.Data.Context
	if _, err := r.db.Exec(`INSERT INTO perfeng_control.integration_requests(principal,issuer,request_id,attempt_issuer,attempt_id,context,disposition,run_id,native_binding) VALUES('alice',$1,$2,$3,$4,$5,$6,$7,$8)`, claims.Request.Issuer, claims.Request.ID, claims.Attempt.Issuer, claims.Attempt.ID, documents[0], documents[1], current.Run.ID, documents[2]); err != nil {
		t.Fatal(err)
	}

	return record
}

func TestIntegrationVerificationAtomicCaptureReplayAndGuards(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	evidence, request := verificationFixture(t)
	message := integrationRequest(t, r, "verification-atomic")
	message.Data.Context.Baseline = nil
	var calls atomic.Int32
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		calls.Add(1)
		return integration.AdmissionResolution{Request: request, Target: run.ExecutionTargetKubernetes, Verification: &evidence,
			Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}, nil
	}
	var wait sync.WaitGroup
	for range 3 {
		wait.Go(func() {
			got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", message, review)
			if err != nil || !reflect.DeepEqual(got.Verification, &evidence) {
				t.Error("atomic capture", err)
			}
		})
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatal("replay re-reviewed evidence", calls.Load())
	}
	reopened := openTest(t, dsn)
	read, err := reopened.GetIntegration(testContext, "alice", message.Data.Context.Request)
	if err != nil || !reflect.DeepEqual(read.Verification, &evidence) {
		t.Fatal("reopened read lost evidence", err)
	}
	read.Verification.PolicyBytes[0] = '!'
	never := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		t.Fatal("historical retry consulted current review")
		return integration.AdmissionResolution{}, run.ErrUnavailable
	}
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", message, never); err != nil || !reflect.DeepEqual(got.Verification, &evidence) {
		t.Fatal("retry changed original evidence", err)
	}
	if _, err := r.GetIntegration(testContext, "bob", message.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("scope leaked", err)
	}
	for _, statement := range []string{
		`UPDATE perfeng_control.integration_requests SET analysis_verification=NULL`,
		`DELETE FROM perfeng_control.integration_requests`,
		`TRUNCATE perfeng_control.integration_requests CASCADE`,
	} {
		_, err := r.db.Exec(statement)
		var blocked interface{ SQLState() string }
		if !errors.As(err, &blocked) || blocked.SQLState() != "55000" {
			t.Fatal("historical mutation accepted", err)
		}
	}
	_, err = r.db.Exec(`INSERT INTO perfeng_control.integration_requests(principal,issuer,request_id,attempt_issuer,attempt_id,context,disposition,run_id,native_binding,analysis_verification) SELECT principal,issuer,request_id,attempt_issuer,attempt_id,context,disposition,run_id,native_binding,jsonb_set(analysis_verification,'{policyBytes}','"e30="'::jsonb) FROM perfeng_control.integration_requests WHERE request_id=$1`, message.Data.Context.Request.ID)
	var constraint interface{ SQLState() string }
	if !errors.As(err, &constraint) || constraint.SQLState() != "23514" {
		t.Fatal("SQL accepted policy bytes outside native pin", err)
	}
	bad := integrationRequest(t, r, "verification-invalid")
	bad.Data.Context.Baseline = nil
	evidence.ContractsVersion = "registry"
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", bad, review); !errors.Is(err, run.ErrUnavailable) || got.Run != nil {
		t.Fatal("invalid snapshot accepted", err)
	}
	if _, err := r.GetIntegration(testContext, "alice", bad.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("rollback leaked request", err)
	}
	var runs int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("rollback leaked Run", err)
	}
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.integration_events`).Scan(&runs); err != nil || runs != 1 {
		t.Fatal("rollback leaked event", err)
	}
}

func migrateBeforeVerification(t *testing.T, r *Repository, boundary string) {
	t.Helper()
	if _, err := r.db.Exec(`CREATE SCHEMA perfeng_control; CREATE TABLE perfeng_control.schema_migrations(version text PRIMARY KEY,sha256 text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= boundary {
			break
		}
		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.db.Exec(string(content)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if _, err := r.db.Exec(`INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES($1,$2)`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationVerificationUpgradePreservesAbsenceWithoutBackfill(t *testing.T) {
	r := openTest(t, testDatabase(t))
	migrateBeforeVerification(t, r, "0022_")
	legacy := seedPreVerificationAdmission(t, r, "verification-upgrade")
	for range 2 {
		if err := r.Migrate(testContext); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.GetIntegration(testContext, "alice", legacy.Disposition.Data.Context.Request)
	if err != nil || got.Verification != nil || !reflect.DeepEqual(got.Native, legacy.Native) {
		t.Fatal("upgrade reconstructed or lost historical evidence", err)
	}
	request := legacy.Disposition
	request.Type, request.Source, request.Time = "dev.perfeng.integration.requested.v1", request.Data.Context.Request.Issuer, request.Data.Context.RequestedAt
	request.Data.Run = nil
	never := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		t.Fatal("legacy replay attempted current review/backfill")
		return integration.AdmissionResolution{}, run.ErrUnavailable
	}
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", legacy.Disposition.Source, request, never); err != nil || got.Verification != nil {
		t.Fatal("legacy replay changed absence", err)
	}
	if _, err := r.db.Exec(`UPDATE perfeng_control.integration_requests SET analysis_verification='{}'::jsonb`); err == nil {
		t.Fatal("SQL augmented historical absence")
	}
}
