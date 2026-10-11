package postgres

import (
	"context"
	"database/sql"
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

func discoverForTest(t *testing.T, repository *Repository, workerID string) nativeworker.Discovery {
	t.Helper()
	discovery, err := repository.DiscoverNativeBaselineAttempt(testContext, workerID)
	if err != nil || !discovery.Valid() {
		t.Fatalf("invalid discovery: %v", err)
	}
	return discovery
}

func nativeDiscoveryRecords(t *testing.T, repository *Repository) map[string]string {
	t.Helper()
	records := make(map[string]string)
	for _, table := range []string{"runs", "native_worker_presence_v2", "native_execution_leases",
		"artifacts", "native_upload_grants", "native_artifacts", "native_terminal_reports"} {
		var value string
		if err := repository.db.QueryRow(`
			SELECT coalesce(jsonb_agg(value ORDER BY value::text), '[]'::jsonb)::text
			FROM (SELECT to_jsonb(t) AS value FROM perfeng_control.` + table + ` AS t) AS records
		`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		records[table] = value
	}
	return records
}

func TestNativeDiscoveryIsScopedReadOnlyAndSurvivesReopening(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	before := nativeDiscoveryRecords(t, fixture.repository)
	active := discoverForTest(t, fixture.repository, fixture.workerID)
	if active.State != nativeworker.DiscoveryFound || active.Availability != "AVAILABLE" ||
		active.Attempt.State != nativeworker.InspectionActive || active.Attempt.RunID != fixture.accepted.Run.ID ||
		active.Attempt.AttemptID != fixture.authority.attemptID || active.Attempt.LeaseID != fixture.authority.leaseID ||
		active.Attempt.RunRevision != fixture.accepted.Run.Revision || !active.SlotExpiresAt.Equal(fixture.authority.expiresAt) {
		t.Fatal("discovery did not observe the owned slot")
	}
	other := discoverForTest(t, fixture.repository, "another-windows-worker")
	if other.State != nativeworker.DiscoveryNone || other.Availability != "UNREPORTED" {
		t.Fatal("foreign attempt was disclosed")
	}
	if !reflect.DeepEqual(before, nativeDiscoveryRecords(t, fixture.repository)) {
		t.Fatal("discovery changed durable state")
	}
	// A new pool, rather than cached attempt state, must observe the same authority.
	reopened := openTest(t, fixture.connection)
	again := discoverForTest(t, reopened, fixture.workerID)
	if again.State != active.State || again.Attempt.AttemptID != active.Attempt.AttemptID ||
		!again.SlotExpiresAt.Equal(*active.SlotExpiresAt) {
		t.Fatal("independent pool lost the durable discovery snapshot")
	}
}

func setDiscoveryAvailability(t *testing.T, repository *Repository, workerID, availability string) time.Time {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	var presence map[string]any
	if err := json.Unmarshal(document, &presence); err != nil {
		t.Fatal(err)
	}
	presence["availability"] = availability
	document, err = json.Marshal(presence)
	if err != nil {
		t.Fatal(err)
	}
	observedAt, err := repository.RecordNativeWorkerV2Presence(testContext,
		httpapi.NativeWorkerPresenceRecord{WorkerID: workerID, ObservedDocument: document,
			Availability: availability, Qualified: false, AuthorizedToAcquire: false})
	if err != nil {
		t.Fatal(err)
	}
	return observedAt
}

func TestNativeDiscoveryPreservesIntentAndExcludesReleasedSlots(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	for _, availability := range []string{"DRAINING", "UNAVAILABLE", "AVAILABLE"} {
		setDiscoveryAvailability(t, fixture.repository, fixture.workerID, availability)
		if _, err := fixture.repository.db.Exec(`
			UPDATE perfeng_control.native_worker_presence_v2
			SET observed_at=clock_timestamp()-interval '1 day' WHERE worker_id=$1
		`, fixture.workerID); err != nil {
			t.Fatal(err)
		}
		discovery := discoverForTest(t, fixture.repository, fixture.workerID)
		if discovery.Availability != availability || discovery.State != nativeworker.DiscoveryFound {
			t.Fatal("discovery confused intent with freshness or eligibility")
		}
	}
	if err := fixture.repository.ReleaseNativeBaselineLease(testContext, fixture.workerID,
		v2ReleaseRequest(fixture, fixture.accepted.Run.Revision, nativeworker.ReleaseWorkerShutdown)); err != nil {
		t.Fatal(err)
	}
	if discoverForTest(t, fixture.repository, fixture.workerID).State != nativeworker.DiscoveryNone {
		t.Fatal("released slot remained current")
	}
}

func TestNativeDiscoveryPrefersTerminalOnlyWhileSlotOccupiesCapacity(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	ack, err := fixture.repository.ReportNativeBaselineTerminal(testContext, fixture.workerID,
		nativeworker.TerminalRequest{
			Kind: nativeworker.TerminalFailed, LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
			ExpectedRunRevision: fixture.accepted.Run.Revision, Capability: fixture.authority.capability,
			Failure: &nativeworker.TerminalFailure{Code: nativeworker.TerminalFailurePreparation,
				Message: "preparation failed", Retryable: false},
		})
	if err != nil {
		t.Fatal(err)
	}
	if discoverForTest(t, fixture.repository, fixture.workerID).State != nativeworker.DiscoveryNone {
		t.Fatal("ordinary terminal acceptance did not expire capacity")
	}
	// Model a retained terminal report whose matching lease still occupies the
	// slot. Normal reporting already expires it; discovery must not change that.
	if _, err := fixture.repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases SET expires_at=$1 WHERE run_id=$2
	`, fixture.authority.expiresAt, fixture.accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	discovery := discoverForTest(t, fixture.repository, fixture.workerID)
	if discovery.State != nativeworker.DiscoveryFound || discovery.Attempt.State != nativeworker.InspectionTerminal ||
		discovery.Attempt.RunRevision != ack.RunRevision || discovery.Attempt.LeaseID != "" {
		t.Fatal("accepted terminal did not take precedence")
	}
	if _, err := fixture.repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '2 days', expires_at=clock_timestamp()-interval '1 day'
		WHERE run_id=$1
	`, fixture.accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if discoverForTest(t, fixture.repository, fixture.workerID).State != nativeworker.DiscoveryNone {
		t.Fatal("historical terminal report was returned as current capacity")
	}
}

func TestNativeDiscoveryFailsClosedOnMultipleCurrentSlots(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	second := acceptNativeRun(t, fixture.repository, "bob", "discovery-ambiguous-0001")
	tx, err := fixture.repository.begin(testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockNativeWorkerCapacity(testContext, tx, fixture.workerID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lockNativeRun(testContext, tx, second.Run.ID); err != nil {
		t.Fatal(err)
	}
	now, err := dbNow(testContext, tx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := json.Marshal(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, written, err := persistNativeLease(testContext, tx, fixture.workerID, second.Run.ID,
		plan, now, 2*time.Minute, 40*time.Second); err != nil || !written {
		t.Fatalf("failed to create inconsistent capacity fixture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.DiscoverNativeBaselineAttempt(testContext, fixture.workerID); err == nil {
		t.Fatal("ambiguous slots were silently truncated")
	}
}

// waitForDiscoveryLock proves the operation is waiting, rather than relying on
// a scheduling delay. The poll observes PostgreSQL's actual lock dependency.
func waitForDiscoveryLock(t *testing.T, database *sql.DB, blocker int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(testContext, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blockedPID int
		err := database.QueryRowContext(ctx, `
			SELECT pid FROM pg_stat_activity
			WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) LIMIT 1
		`, blocker).Scan(&blockedPID)
		if err == nil {
			return blockedPID
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("could not observe lock wait: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("operation never waited on the worker lock")
		case <-ticker.C:
		}
	}
}

func holdDiscoveryWorker(t *testing.T, repository *Repository, workerID string) (*sql.Tx, int) {
	t.Helper()
	tx, err := repository.begin(testContext)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err := lockNativeWorkerCapacity(testContext, tx, workerID); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRowContext(testContext, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func TestNativeDiscoveryReadsFreshClockAndExpiryAfterWaiting(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	tx, pid := holdDiscoveryWorker(t, fixture.repository, fixture.workerID)
	type observation struct {
		value nativeworker.Discovery
		err   error
	}
	result := make(chan observation, 1)
	go func() {
		value, err := fixture.repository.DiscoverNativeBaselineAttempt(testContext, fixture.workerID)
		result <- observation{value, err}
	}()
	waitForDiscoveryLock(t, fixture.repository.db, pid)
	var expiry time.Time
	if err := tx.QueryRowContext(testContext, `
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '1 day', expires_at=clock_timestamp()
		WHERE run_id=$1 RETURNING expires_at
	`, fixture.accepted.Run.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	observed := <-result
	if observed.err != nil || !observed.value.Valid() || observed.value.State != nativeworker.DiscoveryNone ||
		observed.value.ServerTime.Before(expiry) {
		t.Fatalf("discovery returned a pre-wait snapshot or clock: %v", observed.err)
	}
}

func TestNativeDiscoverySerializesEvenBeforeFirstPresence(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "unreported-windows-worker"
	tx, pid := holdDiscoveryWorker(t, repository, workerID)
	result := make(chan error, 1)
	go func() {
		_, err := repository.DiscoverNativeBaselineAttempt(testContext, workerID)
		result <- err
	}()
	waitForDiscoveryLock(t, repository.db, pid)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if discoverForTest(t, repository, workerID).Availability != "UNREPORTED" {
		t.Fatal("discovery fabricated presence")
	}
}

func TestNativeWithdrawalFencesAcquisitionUsingEarlierObservation(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "windows-laptop-01"
	observedAt := authorizeV2WorkerForTest(t, repository, workerID)
	accepted := acceptNativeRun(t, repository, "alice", "discovery-withdrawal-0001")
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := nativeV2PlanForRun(t, accepted.Run)
	tx, pid := holdDiscoveryWorker(t, repository, workerID)
	type acquisition struct {
		acquired bool
		err      error
	}
	result := make(chan acquisition, 1)
	go func() {
		_, acquired, err := repository.AcquireNativeBaselineLease(testContext, workerID, observedAt,
			candidate, plan, 2*time.Minute, 2*time.Minute, 40*time.Second)
		result <- acquisition{acquired, err}
	}()
	waitForDiscoveryLock(t, repository.db, pid)
	if _, err := tx.ExecContext(testContext, `
		UPDATE perfeng_control.native_worker_presence_v2
		SET observed_at=clock_timestamp(), availability='UNAVAILABLE',
			presence=jsonb_set(presence,'{availability}','"UNAVAILABLE"'::jsonb)
		WHERE worker_id=$1
	`, workerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	completed := <-result
	if completed.err != nil || completed.acquired {
		t.Fatalf("in-flight stale acquisition crossed committed withdrawal: %v", completed.err)
	}
	setDiscoveryAvailability(t, repository, workerID, "UNAVAILABLE")
	discovery := discoverForTest(t, repository, workerID)
	if discovery.State != nativeworker.DiscoveryNone || discovery.Availability != "UNAVAILABLE" {
		t.Fatal("withdrawn worker acquired late work")
	}
}

func TestNativeDiscoveryRejectsInvalidIdentityAndCancellation(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DiscoverNativeBaselineAttempt(testContext, "../foreign"); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid worker identity: %v", err)
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := repository.DiscoverNativeBaselineAttempt(ctx, "windows-laptop-01"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery: %v", err)
	}
}

func TestNativeDiscoveryWaitsForAcquisitionCommit(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	workerID := "windows-laptop-01"
	observedAt := authorizeV2WorkerForTest(t, repository, workerID)
	accepted := acceptNativeRun(t, repository, "alice", "discovery-acquisition-race-0001")
	candidate := candidateByID(t, repository, accepted.Run.ID)
	plan := nativeV2PlanForRun(t, accepted.Run)
	tx, err := repository.begin(testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, _, err := lockNativeRun(testContext, tx, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err := tx.QueryRowContext(testContext, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	type acquiredResult struct {
		lease    nativeworker.BaselineLease
		acquired bool
		err      error
	}
	acquisition := make(chan acquiredResult, 1)
	go func() {
		lease, acquired, err := repository.AcquireNativeBaselineLease(testContext, workerID, observedAt,
			candidate, plan, 2*time.Minute, 2*time.Minute, 40*time.Second)
		acquisition <- acquiredResult{lease, acquired, err}
	}()
	acquisitionPID := waitForDiscoveryLock(t, repository.db, blocker)
	type observedResult struct {
		value nativeworker.Discovery
		err   error
	}
	discovered := make(chan observedResult, 1)
	go func() {
		value, err := repository.DiscoverNativeBaselineAttempt(testContext, workerID)
		discovered <- observedResult{value, err}
	}()
	waitForDiscoveryLock(t, repository.db, acquisitionPID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	issued := <-acquisition
	observed := <-discovered
	if issued.err != nil || !issued.acquired || !issued.lease.Valid() || observed.err != nil ||
		!observed.value.Valid() || observed.value.State != nativeworker.DiscoveryFound ||
		observed.value.Attempt.AttemptID != issued.lease.AttemptID {
		t.Fatalf("discovery missed committed acquisition: acquire=%v discover=%v", issued.err, observed.err)
	}
}

func TestNativePresenceReplacementParticipatesInIdentitySerialization(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	tx, pid := holdDiscoveryWorker(t, fixture.repository, fixture.workerID)
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	var presence map[string]any
	if err := json.Unmarshal(document, &presence); err != nil {
		t.Fatal(err)
	}
	presence["availability"] = "UNAVAILABLE"
	document, err = json.Marshal(presence)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := fixture.repository.RecordNativeWorkerV2Presence(testContext,
			httpapi.NativeWorkerPresenceRecord{WorkerID: fixture.workerID,
				ObservedDocument: document, Availability: "UNAVAILABLE"})
		result <- err
	}()
	waitForDiscoveryLock(t, fixture.repository.db, pid)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	discovery := discoverForTest(t, fixture.repository, fixture.workerID)
	if discovery.State != nativeworker.DiscoveryFound || discovery.Availability != "UNAVAILABLE" {
		t.Fatal("presence withdrawal revoked earlier authority or was not visible")
	}
}
