package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func integrationRequest(t *testing.T, r *Repository, suffix string) contract.IntegrationMessage {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-integration/completed.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages []contract.IntegrationMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	message := messages[0]
	var now time.Time
	if err := r.db.QueryRow("SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	message.ID += suffix
	message.Data.Context.Request.ID += suffix
	message.Data.Context.Attempt.ID += suffix
	message.Data.Context.Principal.ID = "alice"
	message.Data.Context.RequestedAt = now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	message.Data.Context.Deadline = now.Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)
	message.Time = message.Data.Context.RequestedAt
	if _, err := integrationcontract.EncodeIntegrationMessage(message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestIntegrationUpgradeConcurrentReplayAndScopedRead(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if _, err := r.db.Exec(`CREATE SCHEMA perfeng_control; CREATE TABLE perfeng_control.schema_migrations(version text PRIMARY KEY,sha256 text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0020_" {
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
	legacy := accepted(t, r, "legacy", "integration-upgrade-native")
	previous := integrationRequest(t, r, "before-native-binding")
	previous.Data.Context.Principal.ID = "legacy"
	disposition := previous
	disposition.Source, disposition.Type = "https://provider.example/identity", "dev.perfeng.integration.accepted.v1"
	disposition.Data.Run = &contract.CatalogIdentity{Issuer: disposition.Source, ID: legacy.Run.ID}
	contextBytes, err := json.Marshal(previous.Data.Context)
	if err != nil {
		t.Fatal(err)
	}
	dispositionBytes, err := json.Marshal(disposition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO perfeng_control.integration_requests(principal,issuer,request_id,attempt_issuer,attempt_id,context,disposition,run_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, "legacy", previous.Data.Context.Request.Issuer, previous.Data.Context.Request.ID, previous.Data.Context.Attempt.Issuer, previous.Data.Context.Attempt.ID, contextBytes, dispositionBytes, legacy.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(testContext, "legacy", legacy.Run.ID); err != nil || !reflect.DeepEqual(got, legacy.Run) {
		t.Fatal("upgrade changed native Run", err)
	}
	if got, err := r.GetIntegration(testContext, "legacy", previous.Data.Context.Request); err != nil || got.Native != nil {
		t.Fatal("upgrade inferred historical binding", err)
	}
	message := integrationRequest(t, r, "concurrent")
	request := testRequest(t)
	var calls atomic.Int32
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		calls.Add(1)
		return request, run.ExecutionTargetKubernetes, nil
	}
	results := make(chan integration.Record, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			result, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, review)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	var original integration.Record
	for result := range results {
		if result.Run == nil {
			t.Fatal("not accepted")
		}
		if original.Run != nil && !reflect.DeepEqual(original.Disposition, result.Disposition) {
			t.Fatal("replay changed disposition")
		}
		original = result
	}
	if calls.Load() != 1 || original.Run == nil {
		t.Fatal("review repeated", calls.Load())
	}
	_, err = r.db.Exec(`UPDATE perfeng_control.integration_requests SET disposition=jsonb_set(disposition,'{data,context,request,id}','"wrong"'::jsonb)`)
	var shapeError interface{ SQLState() string }
	if !errors.As(err, &shapeError) || shapeError.SQLState() != "23514" {
		t.Fatal("database did not enforce immutable context consistency", err)
	}
	if _, err := integrationcontract.EncodeIntegrationMessage(original.Disposition); err != nil {
		t.Fatal(err)
	}
	message.Data.Context.Evidence[0].ID = "caller-mutated"
	message.Data.Context = original.Disposition.Data.Context
	other := openTest(t, dsn)
	if _, err := other.db.Exec(`UPDATE perfeng_control.create_bindings SET expires_at=clock_timestamp()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Cancel(testContext, "alice", original.Run.ID); err != nil {
		t.Fatal(err)
	}
	message.ID += "-redelivery"
	replayed, err := other.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, review)
	if err != nil || replayed.Run.State != run.StateAborted || !reflect.DeepEqual(replayed.Disposition, original.Disposition) || calls.Load() != 1 {
		t.Fatal("retained replay lost original disposition/current state", err)
	}
	if _, err := other.GetIntegration(testContext, "bob", message.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal disclosure", err)
	}
	wrongIssuer := message.Data.Context.Request
	wrongIssuer.Issuer += "/other"
	if _, err := other.GetIntegration(testContext, "alice", wrongIssuer); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("unscoped identity", err)
	}
	for _, newEvent := range []bool{false, true} {
		changed := message
		changed.Data.Context.Service.ID += "-changed"
		if newEvent {
			changed.ID += "-new"
		}
		if _, err := other.AdmitIntegration(testContext, "alice", "https://provider.example/identity", changed, review); !errors.Is(err, run.ErrConflict) {
			t.Fatal("conflicting reuse accepted", err)
		}
	}
	reused := message
	reused.ID += "-attempt-reuse"
	reused.Data.Context.Request.ID += "-new"
	if _, err := other.AdmitIntegration(testContext, "alice", "https://provider.example/identity", reused, review); !errors.Is(err, run.ErrConflict) {
		t.Fatal("attempt reused", err)
	}
	var count int
	if err := other.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs WHERE principal='alice'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("partial/duplicate Run", count, err)
	}
}

func TestIntegrationDurableRejectionAndTransientRetry(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	message := integrationRequest(t, r, "rejection")
	request := testRequest(t)
	reject := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return run.Request{}, "", run.ErrForbidden
	}
	rejected, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, reject)
	if err != nil || rejected.Run != nil || rejected.Disposition.Data.Error.Code != "unavailable_reference" {
		t.Fatal("rejection not retained", err)
	}
	if _, err := integrationcontract.EncodeIntegrationMessage(rejected.Disposition); err != nil {
		t.Fatal(err)
	}
	approve := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return request, run.ExecutionTargetKubernetes, nil
	}
	replay, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, approve)
	if err != nil || !reflect.DeepEqual(replay.Disposition, rejected.Disposition) {
		t.Fatal("rejected identity redispatched", err)
	}
	message = integrationRequest(t, r, "transient")
	transient := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return run.Request{}, "", errors.New("private admission backend detail")
	}
	if _, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, transient); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := r.GetIntegration(testContext, "alice", message.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("transient failure reserved identity", err)
	}
	if got, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, approve); err != nil || got.Run == nil {
		t.Fatal("retry failed", err)
	}
	message = integrationRequest(t, r, "expired")
	message.Data.Context.Deadline = message.Data.Context.RequestedAt
	message.Data.Context.RequestedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	message.Time = message.Data.Context.RequestedAt
	if got, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, approve); err != nil || got.Run != nil || got.Disposition.Data.Error.Code != "expired" {
		t.Fatal("deadline bypass", err)
	}
}

func TestIntegrationApprovalLockAndPostReviewDeadline(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	source, artifact := completedBaselineSource(t, r, "alice", "integration-baseline-source")
	record, err := r.CreateBaseline(testContext, "alice", baselineInput(source, artifact))
	if err != nil {
		t.Fatal(err)
	}
	samples, cv := int64(8), 0.03
	record, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateQualified, Actor: "reviewer", Reason: "qualified", Qualification: &baseline.Qualification{Status: baseline.QualificationPassed, Reasons: []string{}, SampleCount: &samples, MaximumCV: &cv}})
	if err != nil {
		t.Fatal(err)
	}
	record, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateApproved, Actor: "reviewer", Reason: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	message := integrationRequest(t, r, "locked-baseline")
	request := testRequest(t)
	review := func(ctx context.Context, principal string, claims contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		got, err := read(ctx, principal, record.ID, record.Version)
		if err != nil {
			return run.Request{}, "", err
		}
		if got.State != baseline.StateApproved {
			return run.Request{}, "", run.ErrForbidden
		}
		// A competing retirement's row lock must fail while this admission holds approval.
		probe, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return run.Request{}, "", err
		}
		defer probe.Rollback()
		_, err = probe.ExecContext(ctx, `SELECT 1 FROM perfeng_control.baselines WHERE principal=$1 AND baseline_id=$2 AND version=$3 FOR UPDATE NOWAIT`, principal, record.ID, record.Version)
		var lockError interface{ SQLState() string }
		if !errors.As(err, &lockError) || lockError.SQLState() != "55P03" {
			return run.Request{}, "", errors.New("approval lock absent")
		}
		return request, run.ExecutionTargetKubernetes, nil
	}
	if got, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, review); err != nil || got.Run == nil {
		t.Fatal("approval guard", err)
	}
	if _, err := r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "reviewer", Reason: "retired"}); err != nil {
		t.Fatal("approval lock leaked", err)
	}
	message = integrationRequest(t, r, "late-review")
	message.Data.Context.Deadline = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	reviewed := false
	late := func(ctx context.Context, principal string, claims contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		reviewed = true
		// Drive the DB-clock boundary directly rather than assuming a scheduler sleep.
		for {
			var expired bool
			if err := r.db.QueryRowContext(ctx, `SELECT clock_timestamp() >= $1::timestamptz`, claims.Deadline).Scan(&expired); err != nil {
				return run.Request{}, "", err
			}
			if expired {
				break
			}
		}
		return request, run.ExecutionTargetKubernetes, nil
	}
	if got, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", message, late); err != nil || got.Run != nil || got.Disposition.Data.Error.Code != "expired" {
		t.Fatal("deadline not rechecked", err)
	}
	if !reviewed {
		t.Fatal("test never crossed the deadline during review")
	}
}
