package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestBenchmarkCleanupMigrationPreservesExistingHoldAndEnforcesShape(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if _, err := repository.db.Exec(`
		CREATE SCHEMA perfeng_control;
		CREATE TABLE perfeng_control.schema_migrations (
			version text PRIMARY KEY,
			sha256 text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)
	`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0018_" {
			break
		}
		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.db.Exec(string(content)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if _, err := repository.db.Exec(`
			INSERT INTO perfeng_control.schema_migrations (version, sha256) VALUES ($1, $2)
		`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	seed := accepted(t, repository, "alice", "cleanup-upgrade-request")
	intent := benchmarkIntent()
	if _, err := repository.db.Exec(`
		INSERT INTO perfeng_control.benchmark_target_reservations (
			run_id, target_id, cluster_uid, plan, plan_sha256, admission, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp())
	`, seed.Run.ID, intent.TargetID, intent.ClusterUID, intent.Plan, intent.PlanSHA256, []byte("original admission")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Cancel(testContext, "alice", seed.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	claim := oneClaim(t, repository, "cleanup-upgrade-worker")
	stored, found, err := repository.GetBenchmarkReservation(testContext, claim.Lease)
	if err != nil || !found || !stored.Intent.Same(intent) || stored.Cleanup != nil || string(stored.Admission) != "original admission" {
		t.Fatalf("migration changed hold: %v", err)
	}
	for _, assignment := range []string{
		"released_at=clock_timestamp()",
		"cleanup_fenced_at=clock_timestamp()",
		"cleanup_started_at=clock_timestamp()",
		"cleanup_receipt='unbound'::bytea",
	} {
		if _, err := repository.db.Exec(`UPDATE perfeng_control.benchmark_target_reservations SET `+assignment+` WHERE run_id=$1`, claim.Run.ID); err == nil {
			t.Fatalf("invalid cleanup shape accepted: %s", assignment)
		}
	}
	if _, err := repository.BeginBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateAborted}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
}
