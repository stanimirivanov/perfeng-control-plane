package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func acceptNativeRun(t *testing.T, repository *Repository, principal, key string) run.Accepted {
	t.Helper()
	accepted, err := repository.Accept(
		testContext, principal, key, testRequest(t), run.ExecutionTargetNativeLinux,
	)
	if err != nil {
		t.Fatal(err)
	}

	return accepted
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

func candidateByID(t *testing.T, repository *Repository, runID string) nativeworker.Candidate {
	t.Helper()
	candidates, err := repository.NativeCandidates(testContext, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Run.ID == runID {
			return candidate
		}
	}
	t.Fatal("native Run was not returned as a candidate")

	return nativeworker.Candidate{}
}

type nativeV2LeaseFixture struct {
	repository *Repository
	connection string
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
	connection := testDatabase(t)
	repository := openTest(t, connection)
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "linux-workstation-01"
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
		testContext, tx, workerID, accepted.Run.ID,
		planBytes, now, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !written {
		t.Fatalf("v2 lease persistence failed: written=%t, err=%v", written, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	return nativeV2LeaseFixture{
		repository: repository, connection: connection, workerID: workerID,
		accepted: accepted, authority: authority, plan: plan,
	}
}
