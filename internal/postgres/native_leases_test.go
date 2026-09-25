package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func acceptNativeRun(t *testing.T, repository *Repository, principal, key string) run.Accepted {
	t.Helper()
	accepted, err := repository.Accept(
		testContext, principal, key, testRequest(t), run.ExecutionTargetNativeWindows,
	)
	if err != nil {
		t.Fatal(err)
	}

	return accepted
}

func recordEligibleNativeWorker(t *testing.T, repository *Repository, workerID string) {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.RecordNativeWorkerPresence(
		testContext,
		httpapi.NativeWorkerPresenceRecord{
			WorkerID: workerID, ObservedDocument: document, Availability: "AVAILABLE",
			Qualified: true, AuthorizedToAcquire: true,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func postgresNativePlan(t *testing.T) nativeworker.Plan {
	t.Helper()
	template := nativeworker.AssignmentTemplate{
		BaseURL: "https://sut.perfeng.invalid/search", Browser: "edge", DiagnosticMode: "trace",
		ArtifactPlan: []nativeworker.ArtifactSlot{
			{Slot: "measurements", Kind: "measurements", MediaType: "application/json", Format: "playwright-measurements-v2", MaximumBytes: 1 << 20, Required: true},
			{Slot: "manifest", Kind: "manifest", MediaType: "application/json", Format: "raw-result-v2", MaximumBytes: 1 << 20, Required: true},
		},
	}
	plan, err := template.Resolve(
		"checkout-api",
		"browser-smoke",
		"smoke",
		"local-kind",
		rawresult.ExecutionArtifact{
			Kind: "source-checkout", Repository: "https://github.com/example/perfeng-playwright",
			GitSHA: strings.Repeat("a", 40),
			DependencyLock: &rawresult.DependencyLock{
				Path: "pnpm-lock.yaml", SHA256: strings.Repeat("b", 64),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return plan
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

func acquireNativeLeaseForTest(
	t *testing.T,
	repository *Repository,
	principal string,
	key string,
	workerID string,
) (run.Accepted, nativeworker.Lease) {
	t.Helper()
	accepted := acceptNativeRun(t, repository, principal, key)
	recordEligibleNativeWorker(t, repository, workerID)
	observation, err := repository.EligibleNativeWorker(testContext, workerID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := repository.AcquireNativeLease(
		testContext,
		workerID,
		observation.ObservedAt,
		candidateByID(t, repository, accepted.Run.ID),
		postgresNativePlan(t),
		2*time.Minute,
		2*time.Minute,
		40*time.Second,
	)
	if err != nil || !acquired {
		t.Fatal("native lease was not acquired", err)
	}

	return accepted, lease
}

func TestNativeLeaseAuthorityGeneration(t *testing.T) {
	uuidPattern := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	identifier, err := newUUID()
	if err != nil || !uuidPattern.MatchString(identifier) {
		t.Fatal("invalid native lease UUID", err)
	}
	capability, digest, err := newNativeLeaseCapability()
	if err != nil || len(capability) != 64 {
		t.Fatal("invalid native lease capability", err)
	}
	expected := sha256.Sum256([]byte(capability))
	if digest != expected {
		t.Fatal("native lease capability digest does not match")
	}
}

func TestNativeLeaseAcquisitionPersistsOnlyCapabilityDigest(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-lease-request-0001")
	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	observation, err := repository.EligibleNativeWorker(
		testContext, "windows-laptop-01", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := postgresNativePlan(t)
	lease, acquired, err := repository.AcquireNativeLease(
		testContext,
		"windows-laptop-01",
		observation.ObservedAt,
		candidate,
		plan,
		2*time.Minute,
		2*time.Minute,
		40*time.Second,
	)
	if err != nil || !acquired {
		t.Fatal("native lease was not acquired", err)
	}
	uuidPattern := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	if !uuidPattern.MatchString(lease.LeaseID) || !uuidPattern.MatchString(lease.AttemptID) ||
		lease.RunID != accepted.Run.ID || lease.RunRevision != accepted.Run.Revision ||
		lease.RenewAfterSeconds != 40 || len(lease.Capability) != 64 ||
		!lease.ExpiresAt.Equal(lease.IssuedAt.Add(2*time.Minute)) {
		t.Fatal("lease response does not satisfy the native lease contract")
	}

	var storedDigest string
	var storedPlan []byte
	err = repository.db.QueryRow(`
		SELECT encode(capability_sha256,'hex'),plan
		FROM perfeng_control.native_execution_leases
		WHERE run_id=$1
	`, accepted.Run.ID).Scan(&storedDigest, &storedPlan)
	wantDigest := sha256.Sum256([]byte(lease.Capability))
	if err != nil || storedDigest != hex.EncodeToString(wantDigest[:]) ||
		strings.Contains(string(storedPlan), lease.Capability) {
		t.Fatal("native lease secret was not safely persisted", err)
	}
	var persistedPlan nativeworker.Plan
	if json.Unmarshal(storedPlan, &persistedPlan) != nil || persistedPlan.Validate() != nil {
		t.Fatal("persisted native plan is invalid")
	}
	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current != accepted.Run {
		t.Fatal("lease acquisition changed the Run lifecycle", current, err)
	}

	_, acquired, err = repository.AcquireNativeLease(
		testContext,
		"windows-laptop-01",
		observation.ObservedAt,
		candidate,
		plan,
		2*time.Minute,
		2*time.Minute,
		40*time.Second,
	)
	if err != nil || acquired {
		t.Fatal("active native lease was acquired twice", err)
	}
}

func TestNativeLeaseRenewalAdvancesMonotonicRunPhase(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-renewal-request-0001",
		"windows-laptop-01",
	)
	request := nativeworker.RenewalRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision, Phase: nativeworker.PhasePreparing,
		Capability: lease.Capability,
	}
	preparing, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || preparing.RunRevision != 4 || preparing.CancellationRequested ||
		preparing.StopBy != nil || !preparing.ExpiresAt.Equal(preparing.ServerTime.Add(2*time.Minute)) {
		t.Fatalf("preparing renewal = %#v, error = %v", preparing, err)
	}
	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current.State != run.StateWarmingUp || current.Revision != 4 {
		t.Fatalf("preparing Run = %#v, error = %v", current, err)
	}

	request.ExpectedRunRevision = preparing.RunRevision
	unchanged, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || unchanged.RunRevision != preparing.RunRevision ||
		unchanged.ExpiresAt.Before(preparing.ExpiresAt) {
		t.Fatalf("repeated phase renewal = %#v, error = %v", unchanged, err)
	}

	request.Phase = nativeworker.PhaseRunning
	running, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || running.RunRevision != 5 {
		t.Fatalf("running renewal = %#v, error = %v", running, err)
	}
	current, err = repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current.State != run.StateRunning || current.Revision != 5 {
		t.Fatalf("running Run = %#v, error = %v", current, err)
	}

	request.ExpectedRunRevision = running.RunRevision
	request.Phase = nativeworker.PhasePreparing
	if _, err = repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("phase regression error = %v", err)
	}
}

func TestNativeLeaseRenewalReturnsCancellationDeadline(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-renewal-request-0002",
		"windows-laptop-01",
	)
	request := nativeworker.RenewalRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision, Phase: nativeworker.PhasePreparing,
		Capability: lease.Capability,
	}
	preparing, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelling, err := repository.Cancel(testContext, "alice", accepted.Run.ID)
	if err != nil || cancelling.State != run.StateCancelling {
		t.Fatal("native Run was not cancelled", err)
	}
	request.ExpectedRunRevision = cancelling.Revision
	request.Phase = nativeworker.PhaseRunning
	renewal, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", request, 2*time.Minute, 30*time.Second,
	)
	if err != nil || !renewal.CancellationRequested || renewal.StopBy == nil ||
		renewal.RunRevision != cancelling.Revision ||
		!renewal.StopBy.Equal(renewal.ServerTime.Add(30*time.Second)) ||
		!renewal.ExpiresAt.Equal(renewal.ServerTime.Add(2*time.Minute)) ||
		renewal.ServerTime.Before(preparing.ServerTime) {
		t.Fatalf("cancellation renewal = %#v, error = %v", renewal, err)
	}
	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current != cancelling {
		t.Fatal("renewal changed the cancelling Run", err)
	}
}

func TestNativeLeaseRenewalRejectsStaleOrMismatchedAuthority(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-renewal-request-0003",
		"windows-laptop-01",
	)
	valid := nativeworker.RenewalRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision, Phase: nativeworker.PhasePreparing,
		Capability: lease.Capability,
	}

	stale := valid
	stale.ExpectedRunRevision++
	if _, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", stale, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	for name, change := range map[string]func(*nativeworker.RenewalRequest) string{
		"worker": func(*nativeworker.RenewalRequest) string { return "windows-laptop-02" },
		"lease": func(request *nativeworker.RenewalRequest) string {
			request.LeaseID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"attempt": func(request *nativeworker.RenewalRequest) string {
			request.AttemptID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"capability": func(request *nativeworker.RenewalRequest) string {
			request.Capability = strings.Repeat("f", 64)
			return "windows-laptop-01"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			workerID := change(&request)
			if _, err := repository.RenewNativeLease(
				testContext, workerID, request, 2*time.Minute, 30*time.Second,
			); !errors.Is(err, nativeworker.ErrLeaseGone) {
				t.Fatalf("authority error = %v", err)
			}
		})
	}

	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 minute'
		WHERE run_id=$1
	`, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RenewNativeLease(
		testContext, "windows-laptop-01", valid, 2*time.Minute, 30*time.Second,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired lease error = %v", err)
	}
}

func TestNativeLeaseCapacityAndExpiredRecovery(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	second := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	firstRun := acceptNativeRun(t, first, "alice", "native-lease-request-0002")
	secondRun := acceptNativeRun(t, first, "alice", "native-lease-request-0003")
	recordEligibleNativeWorker(t, first, "windows-laptop-01")
	recordEligibleNativeWorker(t, first, "windows-laptop-02")
	firstObservation, err := first.EligibleNativeWorker(
		testContext, "windows-laptop-01", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan := postgresNativePlan(t)
	firstLease, acquired, err := first.AcquireNativeLease(
		testContext, "windows-laptop-01", firstObservation.ObservedAt,
		candidateByID(t, first, firstRun.Run.ID), plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !acquired {
		t.Fatal("first native lease was not acquired", err)
	}
	_, acquired, err = first.AcquireNativeLease(
		testContext, "windows-laptop-01", firstObservation.ObservedAt,
		candidateByID(t, first, secondRun.Run.ID), plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatal("worker capacity admitted a second active lease", err)
	}
	if _, err = first.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET
			issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 minute'
		WHERE run_id=$1
	`, firstRun.Run.ID); err != nil {
		t.Fatal(err)
	}
	secondObservation, err := second.EligibleNativeWorker(
		testContext, "windows-laptop-02", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, acquired, err := second.AcquireNativeLease(
		testContext, "windows-laptop-02", secondObservation.ObservedAt,
		candidateByID(t, second, firstRun.Run.ID), plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || !acquired || recovered.LeaseID == firstLease.LeaseID ||
		recovered.AttemptID == firstLease.AttemptID || recovered.Capability == firstLease.Capability {
		t.Fatal("expired native lease was not replaced with new authority", err)
	}
}

func TestNativeLeaseAcquisitionRechecksPresenceAndCompatibility(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-lease-request-0005")
	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	observation, err := repository.EligibleNativeWorker(
		testContext, "windows-laptop-01", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := postgresNativePlan(t)
	if _, err = repository.db.Exec(`
		UPDATE perfeng_control.native_worker_presence
		SET qualified=false
		WHERE worker_id='windows-laptop-01'
	`); err != nil {
		t.Fatal(err)
	}
	_, acquired, err := repository.AcquireNativeLease(
		testContext, "windows-laptop-01", observation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatal("changed qualification was not rechecked", err)
	}

	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	observation, err = repository.EligibleNativeWorker(
		testContext, "windows-laptop-01", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.Assignment.Browser = "chrome"
	_, acquired, err = repository.AcquireNativeLease(
		testContext, "windows-laptop-01", observation.ObservedAt, candidate, plan,
		2*time.Minute, 2*time.Minute, 40*time.Second,
	)
	if err != nil || acquired {
		t.Fatal("incompatible reviewed plan was acquired", err)
	}
	var leases int
	if err = repository.db.QueryRow(
		"SELECT count(*) FROM perfeng_control.native_execution_leases",
	).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("rejected acquisition persisted a lease", leases, err)
	}
}

func TestConcurrentNativeLeaseAcquisitionHasOneWinner(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	second := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted := acceptNativeRun(t, first, "alice", "native-lease-request-0004")
	recordEligibleNativeWorker(t, first, "windows-laptop-01")
	recordEligibleNativeWorker(t, first, "windows-laptop-02")
	workers := []struct {
		repository  *Repository
		workerID    string
		observation nativeworker.PresenceObservation
		candidate   nativeworker.Candidate
	}{
		{repository: first, workerID: "windows-laptop-01"},
		{repository: second, workerID: "windows-laptop-02"},
	}
	for index := range workers {
		observation, err := workers[index].repository.EligibleNativeWorker(
			testContext, workers[index].workerID, 2*time.Minute,
		)
		if err != nil {
			t.Fatal(err)
		}
		workers[index].observation = observation
		workers[index].candidate = candidateByID(t, workers[index].repository, accepted.Run.ID)
	}
	plan := postgresNativePlan(t)
	type result struct {
		acquired bool
		err      error
	}
	results := make(chan result, len(workers))
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(workers))
	for _, worker := range workers {
		go func(
			repository *Repository,
			workerID string,
			observation nativeworker.PresenceObservation,
			candidate nativeworker.Candidate,
		) {
			ready.Done()
			<-start
			_, acquired, err := repository.AcquireNativeLease(
				context.Background(), workerID, observation.ObservedAt, candidate,
				plan, 2*time.Minute, 2*time.Minute, 40*time.Second,
			)
			results <- result{acquired: acquired, err: err}
		}(worker.repository, worker.workerID, worker.observation, worker.candidate)
	}
	ready.Wait()
	close(start)
	winners := 0
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.acquired {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("native lease winners = %d", winners)
	}
}
