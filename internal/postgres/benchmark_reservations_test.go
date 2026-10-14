package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func benchmarkClaim(t *testing.T, repository *Repository, principal, key string) run.Claim {
	t.Helper()
	accepted(t, repository, principal, key)
	claim := oneClaim(t, repository, "benchmark-worker-"+key)
	var err error
	for _, state := range []run.State{run.StateValidating, run.StateProvisioning} {
		claim.Run, err = repository.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	return claim
}

func benchmarkIntent() run.BenchmarkIntent {
	plan := []byte(`{"approved":"opaque-storage-plan"}`)
	return run.BenchmarkIntent{TargetID: "perfeng-local", ClusterUID: "00000000-0000-4000-8000-000000000001", Plan: plan, PlanSHA256: run.BenchmarkDigest(plan)}
}

func TestBenchmarkReservationPersistsSealedInputsAndFencesCancellation(t *testing.T) {
	repository, dsn := claimsDB(t)
	claim := benchmarkClaim(t, repository, "alice", "benchmark-persist")
	intent := benchmarkIntent()
	first, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("first admission"))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("new observations"))
	if err != nil || !bytes.Equal(retry.Admission, first.Admission) {
		t.Fatalf("admission replaced: %v", err)
	}
	intent.Plan[0] = 'x'
	stored, found, err := repository.GetBenchmarkReservation(testContext, claim.Lease)
	if err != nil || !found || !stored.Intent.Valid() {
		t.Fatalf("aliased stored intent: %v", err)
	}
	uid := "00000000-0000-4000-8000-000000000002"
	stored, err = repository.BindBenchmarkNamespace(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BindBenchmarkNamespace(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BindBenchmarkNamespace(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, "00000000-0000-4000-8000-000000000003"); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("UID replaced: %v", err)
	}
	request := []byte(`{"sealed":"request"}`)
	stored, err = repository.SealBenchmarkRequest(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SealBenchmarkRequest(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, []byte(`{"changed":true}`)); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("request replaced: %v", err)
	}
	restarted := openTest(t, dsn)
	read, found, err := restarted.GetBenchmarkReservation(testContext, claim.Lease)
	if err != nil || !found || read.NamespaceUID != uid || !bytes.Equal(read.Request, request) {
		t.Fatalf("restart lost sealed identity: %v", err)
	}
	if _, err := restarted.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision-1, benchmarkIntent(), []byte("admission")); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := restarted.Cancel(testContext, "alice", claim.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.SealBenchmarkRequest(testContext, claim.Lease, claim.Run.Revision, stored.Intent.PlanSHA256, request); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("cancellation did not win: %v", err)
	}
	if _, err := restarted.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision+1, run.Change{State: run.StateAborted}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := restarted.db.QueryRow(`SELECT count(*) FROM perfeng_control.benchmark_target_reservations WHERE run_id=$1`, claim.Run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal Run released hold: %d, %v", count, err)
	}
	recovery := oneClaim(t, restarted, "retained-terminal-recovery")
	if recovery.Run.ID != claim.Run.ID {
		t.Fatal("terminal hold was not eligible for cleanup")
	}
	other := benchmarkClaim(t, restarted, "bob", "benchmark-other-run")
	if _, err := restarted.ReserveBenchmark(testContext, other.Lease, other.Run.Revision, benchmarkIntent(), []byte("admission")); !errors.Is(err, run.ErrTargetReserved) {
		t.Fatalf("terminal target stolen: %v", err)
	}
}

func TestBenchmarkReservationSurvivesLeaseExpiryAndClusterReplacement(t *testing.T) {
	repository, dsn := claimsDB(t)
	old := benchmarkClaim(t, repository, "alice", "benchmark-expiry")
	intent := benchmarkIntent()
	if _, err := repository.ReserveBenchmark(testContext, old.Lease, old.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE perfeng_control.reconciliation_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`, old.Run.ID); err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, dsn)
	fresh := oneClaim(t, restarted, "replacement-worker")
	if fresh.Lease.Token == old.Lease.Token {
		t.Fatal("worker token reused")
	}
	if _, err := repository.ReserveBenchmark(testContext, old.Lease, old.Run.Revision-1, intent, []byte("admission")); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("lease loss precedence: %v", err)
	}
	if _, err := restarted.ReserveBenchmark(testContext, fresh.Lease, fresh.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatal(err)
	}
	changed := benchmarkIntent()
	changed.ClusterUID = "00000000-0000-4000-8000-000000000004"
	if _, err := restarted.ReserveBenchmark(testContext, fresh.Lease, fresh.Run.Revision, changed, []byte("admission")); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("cluster incarnation replaced: %v", err)
	}
	forged := fresh.Lease
	forged.Principal = "bob"
	if _, _, err := restarted.GetBenchmarkReservation(testContext, forged); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("cross-principal read: %v", err)
	}
	if err := restarted.ReleaseClaim(testContext, fresh.Lease, time.Minute); err != nil {
		t.Fatal(err)
	}
	other := benchmarkClaim(t, restarted, "bob", "benchmark-incarnation")
	if _, err := restarted.ReserveBenchmark(testContext, other.Lease, other.Run.Revision, changed, []byte("admission")); !errors.Is(err, run.ErrTargetReserved) {
		t.Fatalf("new cluster bypassed logical hold: %v", err)
	}
}

func TestBenchmarkConcurrentTargetReservationsHaveOneWinner(t *testing.T) {
	repository, dsn := claimsDB(t)
	claims := []run.Claim{benchmarkClaim(t, repository, "alice", "benchmark-race-a"), benchmarkClaim(t, repository, "bob", "benchmark-race-b")}
	other := openTest(t, dsn)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index, connection := range []*Repository{repository, other} {
		workers.Go(func() {
			<-start
			_, err := connection.ReserveBenchmark(testContext, claims[index].Lease, claims[index].Run.Revision, benchmarkIntent(), []byte("admission"))
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	won, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, run.ErrTargetReserved):
			rejected++
		default:
			t.Fatal(err)
		}
	}
	if won != 1 || rejected != 1 {
		t.Fatalf("winners=%d rejected=%d", won, rejected)
	}
}

func TestBenchmarkReservationMigrationUpgrade(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if _, err := repository.db.Exec(`CREATE SCHEMA perfeng_control; CREATE TABLE perfeng_control.schema_migrations (version text PRIMARY KEY, sha256 text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0017_" {
			break
		}
		data, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if _, err := repository.db.Exec(`INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES($1,$2)`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	seed := accepted(t, repository, "alice", "benchmark-upgrade")
	for _, state := range []run.State{run.StateValidating, run.StateProvisioning} {
		var err error
		seed.Run, err = repository.Advance(testContext, "alice", seed.Run.ID, seed.Run.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	claim := oneClaim(t, repository, "benchmark-upgrade-worker")
	if _, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, benchmarkIntent(), []byte("admission")); err != nil {
		t.Fatal(err)
	}
	for index, assignment := range []string{"target_id='INVALID'", "plan_sha256='wrong'", "admission=''::bytea", "request='unbound'::bytea"} {
		if _, err := repository.db.Exec(fmt.Sprintf("UPDATE perfeng_control.benchmark_target_reservations SET %s WHERE run_id=$1", assignment), claim.Run.ID); err == nil {
			t.Fatalf("constraint case %d accepted", index)
		}
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatalf("migration retry: %v", err)
	}
}
