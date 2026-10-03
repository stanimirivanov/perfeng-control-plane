package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func recordV2WorkerForTest(t *testing.T, repository *Repository, workerID string) time.Time {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	observedAt, err := repository.RecordNativeWorkerV2Presence(testContext,
		httpapi.NativeWorkerPresenceRecord{
			WorkerID: workerID, ObservedDocument: document,
			Availability: "AVAILABLE", Qualified: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return observedAt
}

func allowV2AcquisitionInDisposableTest(t *testing.T, repository *Repository) {
	t.Helper()
	// Production migration 0013 keeps this gate closed. Only this isolated test
	// database drops it to exercise the acquisition transaction before rollout.
	if _, err := repository.db.Exec(`
		ALTER TABLE perfeng_control.native_worker_presence_v2
		DROP CONSTRAINT native_worker_presence_v2_authorized_to_acquire_check
	`); err != nil {
		t.Fatal(err)
	}
}

func authorizeV2WorkerForTest(t *testing.T, repository *Repository, workerID string) time.Time {
	t.Helper()
	observedAt := recordV2WorkerForTest(t, repository, workerID)
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_worker_presence_v2
		SET authorized_to_acquire=true WHERE worker_id=$1
	`, workerID); err != nil {
		t.Fatal(err)
	}

	return observedAt
}

func nativeV2PlanForRun(t *testing.T, current run.Run) nativeworker.BaselinePlan {
	t.Helper()
	plan := pinnedNativeV2Plan(t)
	plan.Assignment.CatalogueTestID = current.Request.TestSuite
	plan.Assignment.Profile = current.Request.Profile
	plan.Assignment.Target.EnvironmentID = current.Request.Environment.ID
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	return plan
}

func TestNativeBaselineAcquisitionGateRemainsClosed(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "windows-laptop-01"
	observedAt := recordV2WorkerForTest(t, repository, workerID)
	if _, err := repository.EligibleNativeWorkerV2(testContext, workerID, 2*time.Minute); !errors.Is(err, run.ErrNotFound) {
		t.Fatalf("closed v2 gate exposed worker eligibility: %v", err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-v2-gate-0001")
	lease, acquired, err := repository.AcquireNativeBaselineLease(
		testContext, workerID, observedAt,
		candidateByID(t, repository, accepted.Run.ID), nativeV2PlanForRun(t, accepted.Run),
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired || lease.LeaseID != "" {
		t.Fatalf("closed gate issued v2 lease: acquired=%t, err=%v", acquired, err)
	}
	var count int
	if err := repository.db.QueryRow(`
		SELECT count(*) FROM perfeng_control.native_execution_leases
	`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("closed gate persisted %d leases: %v", count, err)
	}
}

func TestNativeBaselineAcquisitionFencesCapacityAndExpiry(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	allowV2AcquisitionInDisposableTest(t, repository)
	workerID := "windows-laptop-01"
	authorizeV2WorkerForTest(t, repository, workerID)
	observation, err := repository.EligibleNativeWorkerV2(testContext, workerID, 2*time.Minute)
	if err != nil || observation.Presence.ProtocolVersion != 2 {
		t.Fatalf("v2 observation = %#v, err=%v", observation, err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-v2-acquire-0001")
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := nativeV2PlanForRun(t, accepted.Run)
	lease, acquired, err := repository.AcquireNativeBaselineLease(
		testContext, workerID, observation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !acquired || !lease.Valid() {
		t.Fatalf("v2 lease not acquired: acquired=%t, err=%v", acquired, err)
	}
	var version int
	var planBytes []byte
	var digest []byte
	if err := repository.db.QueryRow(`
		SELECT protocol_version,plan,capability_sha256
		FROM perfeng_control.native_execution_leases WHERE run_id=$1
	`, accepted.Run.ID).Scan(&version, &planBytes, &digest); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte(lease.Capability))
	if version != nativeLeaseProtocolV2 || !reflect.DeepEqual(digest, expectedDigest[:]) ||
		!reflect.DeepEqual(lease.BaselinePlan, plan) {
		t.Fatal("v2 lease lost protocol, plan or capability digest")
	}
	var storedPlan nativeworker.BaselinePlan
	if err := json.Unmarshal(planBytes, &storedPlan); err != nil || !reflect.DeepEqual(storedPlan, plan) {
		t.Fatalf("stored v2 plan differs from reviewed plan: %v", err)
	}
	second := acceptNativeRun(t, repository, "alice", "native-v2-acquire-0002")
	_, acquired, err = repository.AcquireNativeBaselineLease(
		testContext, workerID, observation.ObservedAt,
		candidateByID(t, repository, second.Run.ID), nativeV2PlanForRun(t, second.Run),
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatalf("occupied worker acquired a second Run: acquired=%t, err=%v", acquired, err)
	}
	otherWorker := "windows-laptop-02"
	authorizeV2WorkerForTest(t, repository, otherWorker)
	otherObservation, err := repository.EligibleNativeWorkerV2(testContext, otherWorker, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, acquired, err = repository.AcquireNativeBaselineLease(
		testContext, otherWorker, otherObservation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatalf("active Run was leased twice: acquired=%t, err=%v", acquired, err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1
	`, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	recovered, acquired, err := repository.AcquireNativeBaselineLease(
		testContext, otherWorker, otherObservation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !acquired || recovered.LeaseID == lease.LeaseID {
		t.Fatalf("expired lease was not fenced by a new attempt: acquired=%t, err=%v", acquired, err)
	}
}

func TestNativeBaselineAcquisitionRejectsStaleSelection(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	allowV2AcquisitionInDisposableTest(t, repository)
	workerID := "windows-laptop-01"
	authorizeV2WorkerForTest(t, repository, workerID)
	observation, err := repository.EligibleNativeWorkerV2(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-v2-stale-0001")
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := nativeV2PlanForRun(t, accepted.Run)
	wrongPlan := plan
	wrongPlan.Assignment.Target.EnvironmentID = "different-sut"
	if _, acquired, err := repository.AcquireNativeBaselineLease(
		testContext, workerID, observation.ObservedAt, candidate, wrongPlan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	); err != nil || acquired {
		t.Fatalf("wrong Run context was leased: acquired=%t, err=%v", acquired, err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_worker_presence_v2
		SET observed_at=observed_at+interval '1 microsecond'
		WHERE worker_id=$1
	`, workerID); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := repository.AcquireNativeBaselineLease(
		testContext, workerID, observation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	); err != nil || acquired {
		t.Fatalf("stale observation was leased: acquired=%t, err=%v", acquired, err)
	}
}

func TestNativeWorkerCapacitySerializesAcrossGenerations(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	allowV2AcquisitionInDisposableTest(t, repository)
	workerID := "windows-laptop-01"
	authorizeV2WorkerForTest(t, repository, workerID)
	recordEligibleNativeWorker(t, repository, workerID)
	v1Observation, err := repository.EligibleNativeWorker(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	v2Observation, err := repository.EligibleNativeWorkerV2(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	v1 := acceptNativeRun(t, repository, "alice", "native-v1-race-0001")
	v2 := acceptNativeRun(t, repository, "alice", "native-v2-race-0001")
	v1Candidate := candidateByID(t, repository, v1.Run.ID)
	v2Candidate := candidateByID(t, repository, v2.Run.ID)
	v1Plan := postgresNativePlan(t)
	v2Plan := nativeV2PlanForRun(t, v2.Run)
	type outcome struct {
		acquired bool
		err      error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_, acquired, err := repository.AcquireNativeLease(
			testContext, workerID, v1Observation.ObservedAt, v1Candidate, v1Plan,
			2*time.Minute, 2*time.Minute, 40*time.Second,
		)
		results <- outcome{acquired, err}
	}()
	go func() {
		defer workers.Done()
		<-start
		_, acquired, err := repository.AcquireNativeBaselineLease(
			testContext, workerID, v2Observation.ObservedAt, v2Candidate, v2Plan,
			2*time.Minute, 2*time.Minute, 40*time.Second,
		)
		results <- outcome{acquired, err}
	}()
	close(start)
	workers.Wait()
	close(results)
	count := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.acquired {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("one worker acquired %d simultaneous Runs", count)
	}
}
