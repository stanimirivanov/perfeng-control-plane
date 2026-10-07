package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
)

func TestNativeAcquisitionMigrationPreservesPresence(t *testing.T) {
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
		if entry.Name() == "0016_native_worker_v2_acquisition.sql" {
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
		if _, err := repository.db.Exec(`
			INSERT INTO perfeng_control.schema_migrations(version,sha256)
			VALUES ($1,$2)
		`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	record := httpapi.NativeWorkerPresenceRecord{
		WorkerID: "windows-laptop-01", ObservedDocument: document,
		Availability: "AVAILABLE", Qualified: true,
	}
	before, err := repository.RecordNativeWorkerV2Presence(testContext, record)
	if err != nil {
		t.Fatal(err)
	}
	record.AuthorizedToAcquire = true
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); err == nil {
		t.Fatal("pre-upgrade database allowed acquisition")
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	allowV2AcquisitionInDisposableTest(t, repository)
	var retained bool
	if err := repository.db.QueryRow(`
		SELECT observed_at=$1 AND qualified AND NOT authorized_to_acquire
		FROM perfeng_control.native_worker_presence_v2 WHERE worker_id=$2
	`, before, record.WorkerID).Scan(&retained); err != nil || !retained {
		t.Fatalf("pre-upgrade presence not retained: retained=%t, err=%v", retained, err)
	}
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); err != nil {
		t.Fatalf("post-upgrade acquisition report rejected: %v", err)
	}
}
