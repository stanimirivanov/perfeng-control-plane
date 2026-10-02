package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeWorkerV2PresenceUpgradePreservesV1(t *testing.T) {
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
		if entry.Name() == "0013_native_worker_v2_presence.sql" {
			break
		}
		document, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.db.Exec(string(document)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(document)
		if _, err := repository.db.Exec(`
			INSERT INTO perfeng_control.schema_migrations(version,sha256)
			VALUES ($1,$2)
		`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	v1, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	record := httpapi.NativeWorkerPresenceRecord{
		WorkerID: "windows-laptop-01", Availability: "AVAILABLE",
		Qualified: true, AuthorizedToAcquire: true,
	}
	record.ObservedDocument = v1
	if _, err := repository.RecordNativeWorkerPresence(testContext, record); err != nil {
		t.Fatal(err)
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	record.ObservedDocument = v2
	record.AuthorizedToAcquire = false
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_worker_presence_v2
		SET authorized_to_acquire=true WHERE worker_id=$1
	`, record.WorkerID); err == nil {
		t.Fatal("database allowed v2 acquisition before the reviewed rollout")
	}
	if _, err := repository.db.Exec(`
		INSERT INTO perfeng_control.native_worker_presence_v2(
			worker_id,observed_at,availability,qualified,authorized_to_acquire,presence
		) VALUES ('other-worker',clock_timestamp(),'AVAILABLE',false,false,$1)
	`, v1); err == nil {
		t.Fatal("database accepted a v1 document in v2 presence")
	}
	var v1Version string
	if err := repository.db.QueryRow(`
		SELECT presence->>'protocolVersion'
		FROM perfeng_control.native_worker_presence WHERE worker_id=$1
	`, record.WorkerID).Scan(&v1Version); err != nil || v1Version != "1" {
		t.Fatalf("v1 protocol version = %q, err = %v", v1Version, err)
	}
	var v2Version string
	if err := repository.db.QueryRow(`
		SELECT presence->>'protocolVersion'
		FROM perfeng_control.native_worker_presence_v2 WHERE worker_id=$1
	`, record.WorkerID).Scan(&v2Version); err != nil || v2Version != "2" {
		t.Fatalf("v2 protocol version = %q, err = %v", v2Version, err)
	}
	observation, err := repository.EligibleNativeWorker(testContext, record.WorkerID, 2*time.Minute)
	if err != nil || observation.Presence.ProtocolVersion != 1 {
		t.Fatalf("v1 scheduling changed after v2 report: %#v, %v", observation, err)
	}
	if _, err := repository.RecordNativeWorkerV2Presence(testContext,
		httpapi.NativeWorkerPresenceRecord{WorkerID: record.WorkerID, ObservedDocument: v1,
			Availability: "AVAILABLE", Qualified: true},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("v1 document accepted by v2 storage: %v", err)
	}
	if _, err := repository.RecordNativeWorkerPresence(testContext, record); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("v2 document accepted by v1 storage: %v", err)
	}
	record.AuthorizedToAcquire = true
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("v2 acquisition enabled before v2 leasing: %v", err)
	}
}
