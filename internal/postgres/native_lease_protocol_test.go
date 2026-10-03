package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeLeaseProtocolUpgradePreservesV1(t *testing.T) {
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
		if entry.Name() == "0014_native_lease_protocol_version.sql" {
			break
		}
		document, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repository.db.Exec(string(document)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(document)
		if _, err = repository.db.Exec(`
			INSERT INTO perfeng_control.schema_migrations(version,sha256)
			VALUES ($1,$2)
		`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}

	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	first := acceptNativeRun(t, repository, "alice", "native-protocol-upgrade-0001")
	insertOldStyleNativeLease(t, repository, first, "windows-laptop-01")
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	var version int
	var v1Worker string
	var v2Worker *string
	if err := repository.db.QueryRow(`
		SELECT protocol_version,worker_v1_id,worker_v2_id
		FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, first.Run.ID).Scan(&version, &v1Worker, &v2Worker); err != nil ||
		version != nativeLeaseProtocolV1 || v1Worker != "windows-laptop-01" || v2Worker != nil {
		t.Fatalf("upgraded v1 lease = (%d, %q, %v), error = %v", version, v1Worker, v2Worker, err)
	}

	second := acceptNativeRun(t, repository, "alice", "native-protocol-upgrade-0002")
	insertOldStyleNativeLease(t, repository, second, "windows-laptop-01")
	if err := repository.db.QueryRow(`
		SELECT protocol_version FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, second.Run.ID).Scan(&version); err != nil || version != nativeLeaseProtocolV1 {
		t.Fatalf("old-style v1 lease version = %d, error = %v", version, err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET worker_id='missing-v1-worker' WHERE run_id=$1
	`, second.Run.ID); err == nil {
		t.Fatal("v1 lease lost its worker foreign key")
	}
}

func insertOldStyleNativeLease(
	t *testing.T,
	repository *Repository,
	accepted run.Accepted,
	workerID string,
) nativeworker.Lease {
	t.Helper()
	plan := postgresNativePlan(t)
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	leaseID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	capability, digest, err := newNativeLeaseCapability()
	if err != nil {
		t.Fatal(err)
	}
	var issuedAt time.Time
	if err := repository.db.QueryRow("SELECT clock_timestamp()").Scan(&issuedAt); err != nil {
		t.Fatal(err)
	}
	issuedAt = issuedAt.UTC()
	expiresAt := issuedAt.Add(2 * time.Minute)
	if _, err := repository.db.Exec(`
		INSERT INTO perfeng_control.native_execution_leases(
			run_id,lease_id,attempt_id,worker_id,capability_sha256,
			issued_at,expires_at,renew_after_seconds,plan
		) VALUES ($1,$2,$3,$4,$5,$6,$7,40,$8)
	`, accepted.Run.ID, leaseID, attemptID, workerID, digest[:], issuedAt,
		expiresAt, planBytes); err != nil {
		t.Fatal(err)
	}
	lease := nativeworker.Lease{
		LeaseID: leaseID, AttemptID: attemptID, RunID: accepted.Run.ID,
		RunRevision: accepted.Run.Revision, IssuedAt: issuedAt,
		ExpiresAt: expiresAt, RenewAfterSeconds: 40, Plan: plan,
		Capability: capability,
	}
	if !lease.Valid() {
		t.Fatal("invalid old-style native lease fixture")
	}

	return lease
}

type nativeV2LeaseFixture struct {
	repository *Repository
	workerID   string
	accepted   run.Accepted
	authority  nativeLeaseAuthority
	plan       nativeworker.BaselinePlan
}

func pinnedNativeV2Plan(t *testing.T) nativeworker.BaselinePlan {
	t.Helper()
	leaseExample, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/lease.json")
	if err != nil {
		t.Fatal(err)
	}
	var plan nativeworker.BaselinePlan
	if err := json.Unmarshal(leaseExample, &plan); err != nil || plan.Validate() != nil {
		t.Fatalf("invalid pinned v2 lease plan: %v", err)
	}

	return plan
}

func TestPinnedNativeV2LeasePlanIsValid(t *testing.T) {
	pinnedNativeV2Plan(t)
}

func newNativeV2LeaseFixture(t *testing.T) nativeV2LeaseFixture {
	t.Helper()
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "windows-laptop-01"
	presence, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordNativeWorkerV2Presence(testContext,
		httpapi.NativeWorkerPresenceRecord{
			WorkerID: workerID, ObservedDocument: presence,
			Availability: "AVAILABLE", Qualified: true,
		},
	); err != nil {
		t.Fatal(err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-v2-persistence-0001")
	plan := pinnedNativeV2Plan(t)
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := repository.begin(testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var lockedWorker string
	if err := tx.QueryRowContext(testContext, `
		SELECT worker_id FROM perfeng_control.native_worker_presence_v2
		WHERE worker_id=$1 FOR UPDATE
	`, workerID).Scan(&lockedWorker); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lockNativeRun(testContext, tx, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	now, err := dbNow(testContext, tx)
	if err != nil {
		t.Fatal(err)
	}
	authority, written, err := persistNativeLease(
		testContext, tx, workerID, accepted.Run.ID, nativeLeaseProtocolV2,
		planBytes, now, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !written {
		t.Fatalf("v2 lease persistence failed: written=%t, err=%v", written, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	return nativeV2LeaseFixture{repository, workerID, accepted, authority, plan}
}

func TestNativeV2LeasePersistsPlanAndWorkerIdentity(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	var storedPlan []byte
	var storedDigest []byte
	var version int
	var v1Worker *string
	var v2Worker string
	if err := fixture.repository.db.QueryRow(`
		SELECT protocol_version,worker_v1_id,worker_v2_id,plan,capability_sha256
		FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, fixture.accepted.Run.ID).Scan(&version, &v1Worker, &v2Worker, &storedPlan, &storedDigest); err != nil {
		t.Fatal(err)
	}
	capabilityDigest := sha256.Sum256([]byte(fixture.authority.capability))
	var stored nativeworker.BaselinePlan
	if version != nativeLeaseProtocolV2 || v1Worker != nil || v2Worker != fixture.workerID ||
		!reflect.DeepEqual(storedDigest, capabilityDigest[:]) ||
		json.Unmarshal(storedPlan, &stored) != nil || !reflect.DeepEqual(stored, fixture.plan) {
		t.Fatal("v2 lease did not retain its version, worker, plan and capability digest")
	}
	if _, err := fixture.repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET worker_id='missing-v2-worker' WHERE run_id=$1
	`, fixture.accepted.Run.ID); err == nil {
		t.Fatal("v2 lease lost its worker foreign key")
	}
}

func TestNativeV2LeaseRejectsV1MutationsAndCompetesWithV1(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository, workerID := fixture.repository, fixture.workerID
	recordEligibleNativeWorker(t, repository, workerID)
	_, err := repository.RenewNativeLease(testContext, workerID, nativeworker.RenewalRequest{
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: fixture.accepted.Run.Revision,
		Phase:               nativeworker.PhasePreparing,
		Capability:          fixture.authority.capability,
	}, 2*time.Minute, 30*time.Second)
	if !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("v1 renewal accepted v2 authority: %v", err)
	}
	err = repository.ReleaseNativeLease(testContext, workerID, nativeworker.ReleaseRequest{
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: fixture.accepted.Run.Revision,
		Reason:              nativeworker.ReleaseWorkerShutdown,
		Capability:          fixture.authority.capability,
	})
	if !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("v1 release accepted v2 authority: %v", err)
	}
	if candidates, err := repository.NativeCandidates(testContext, 100); err != nil {
		t.Fatal(err)
	} else {
		for _, candidate := range candidates {
			if candidate.Run.ID == fixture.accepted.Run.ID {
				t.Fatal("v2-leased Run remained a native candidate")
			}
		}
	}
	observation, err := repository.EligibleNativeWorker(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second := acceptNativeRun(t, repository, "alice", "native-v2-persistence-0002")
	_, acquired, err := repository.AcquireNativeLease(
		testContext, workerID, observation.ObservedAt,
		nativeworker.Candidate{Principal: "alice", Run: second.Run},
		postgresNativePlan(t), 2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatalf("v1 acquisition ignored v2 worker occupancy: acquired=%t, err=%v", acquired, err)
	}
	otherWorker := "windows-laptop-02"
	recordEligibleNativeWorker(t, repository, otherWorker)
	otherObservation, err := repository.EligibleNativeWorker(
		testContext, otherWorker, 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, acquired, err = repository.AcquireNativeLease(
		testContext, otherWorker, otherObservation.ObservedAt,
		nativeworker.Candidate{Principal: "alice", Run: fixture.accepted.Run},
		postgresNativePlan(t), 2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatalf("v1 acquisition replaced active v2 Run: acquired=%t, err=%v", acquired, err)
	}
}

func TestNativeV2LeaseExpiryAllowsV1Recovery(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository, workerID := fixture.repository, fixture.workerID
	recordEligibleNativeWorker(t, repository, workerID)
	observation, err := repository.EligibleNativeWorker(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1
	`, fixture.accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	_, acquired, err := repository.AcquireNativeLease(
		testContext, workerID, observation.ObservedAt,
		nativeworker.Candidate{Principal: "alice", Run: fixture.accepted.Run},
		postgresNativePlan(t), 2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !acquired {
		t.Fatalf("expired v2 lease blocked v1 recovery: acquired=%t, err=%v", acquired, err)
	}
	var version int
	if err := repository.db.QueryRow(`
		SELECT protocol_version FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, fixture.accepted.Run.ID).Scan(&version); err != nil || version != nativeLeaseProtocolV1 {
		t.Fatalf("recovered v1 lease version = %d, err = %v", version, err)
	}
}
