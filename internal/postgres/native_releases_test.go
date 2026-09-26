package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeLeaseReleaseCompletesCancellationIdempotently(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-release-request-0001",
		"windows-laptop-01",
	)
	preparing, err := repository.RenewNativeLease(
		testContext,
		"windows-laptop-01",
		nativeworker.RenewalRequest{
			LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
			ExpectedRunRevision: lease.RunRevision, Phase: nativeworker.PhasePreparing,
			Capability: lease.Capability,
		},
		2*time.Minute,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelling, err := repository.Cancel(testContext, "alice", accepted.Run.ID)
	if err != nil || cancelling.State != run.StateCancelling {
		t.Fatal("native Run was not cancelled", err)
	}
	request := nativeworker.ReleaseRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: cancelling.Revision,
		Reason:              nativeworker.ReleaseCancellationRequested,
		Message:             "browser process tree stopped",
		Capability:          lease.Capability,
	}
	if err = repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", request,
	); err != nil {
		t.Fatal(err)
	}

	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current.State != run.StateAborted ||
		current.Revision != cancelling.Revision+1 || current.FinishedAt == nil {
		t.Fatalf("released Run = %#v, error = %v", current, err)
	}
	var releasedAt time.Time
	var expiresAt time.Time
	var reason string
	var message string
	err = repository.db.QueryRow(`
		SELECT released_at,expires_at,release_reason,release_message
		FROM perfeng_control.native_execution_leases
		WHERE run_id=$1
	`, accepted.Run.ID).Scan(&releasedAt, &expiresAt, &reason, &message)
	if err != nil || !releasedAt.Equal(expiresAt) ||
		reason != string(request.Reason) || message != request.Message {
		t.Fatal("release marker was not persisted", err)
	}

	duplicate := request
	duplicate.Message = "retry detail is not part of idempotency"
	if err = repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", duplicate,
	); err != nil {
		t.Fatal("identical release was not idempotent", err)
	}
	conflicting := request
	conflicting.Reason = nativeworker.ReleaseWorkerShutdown
	if err = repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", conflicting,
	); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("conflicting release error = %v", err)
	}
	if _, err = repository.RenewNativeLease(
		testContext,
		"windows-laptop-01",
		nativeworker.RenewalRequest{
			LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
			ExpectedRunRevision: preparing.RunRevision,
			Phase:               nativeworker.PhaseRunning, Capability: lease.Capability,
		},
		2*time.Minute,
		30*time.Second,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("released lease renewed: %v", err)
	}
}

func TestNativeLeasePreparationReleaseCanBeReacquired(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-release-request-0002",
		"windows-laptop-01",
	)
	request := nativeworker.ReleaseRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision,
		Reason:              nativeworker.ReleasePreparationFailed,
		Capability:          lease.Capability,
	}
	if err := repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", request,
	); err != nil {
		t.Fatal(err)
	}
	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current != accepted.Run {
		t.Fatal("coordination-only release changed the Run", err)
	}

	observation, err := repository.EligibleNativeWorker(
		testContext, "windows-laptop-01", 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, acquired, err := repository.AcquireNativeLease(
		testContext,
		"windows-laptop-01",
		observation.ObservedAt,
		candidateByID(t, repository, accepted.Run.ID),
		postgresNativePlan(t),
		2*time.Minute,
		2*time.Minute,
		40*time.Second,
	)
	if err != nil || !acquired || recovered.LeaseID == lease.LeaseID ||
		recovered.AttemptID == lease.AttemptID || recovered.Capability == lease.Capability {
		t.Fatal("released native lease was not replaced with new authority", err)
	}
	var released bool
	if err = repository.db.QueryRow(`
		SELECT released_at IS NOT NULL
		FROM perfeng_control.native_execution_leases
		WHERE run_id=$1
	`, accepted.Run.ID).Scan(&released); err != nil || released {
		t.Fatal("reacquisition retained the old release marker", err)
	}
	if err = repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", request,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("replaced authority released its successor: %v", err)
	}
}

func TestNativeLeaseReleaseRejectsStaleAndMismatchedAuthority(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-release-request-0003",
		"windows-laptop-01",
	)
	valid := nativeworker.ReleaseRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision,
		Reason:              nativeworker.ReleaseWorkerShutdown,
		Capability:          lease.Capability,
	}
	stale := valid
	stale.ExpectedRunRevision++
	if err := repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", stale,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	for name, change := range map[string]func(*nativeworker.ReleaseRequest) string{
		"worker": func(*nativeworker.ReleaseRequest) string { return "windows-laptop-02" },
		"lease": func(request *nativeworker.ReleaseRequest) string {
			request.LeaseID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"attempt": func(request *nativeworker.ReleaseRequest) string {
			request.AttemptID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"capability": func(request *nativeworker.ReleaseRequest) string {
			request.Capability = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			return "windows-laptop-01"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			workerID := change(&request)
			if err := repository.ReleaseNativeLease(
				testContext, workerID, request,
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
	if err := repository.ReleaseNativeLease(
		testContext, "windows-laptop-01", valid,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired lease error = %v", err)
	}
}

func TestConcurrentNativeLeaseReleaseHasOneDurableReason(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	second := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, lease := acquireNativeLeaseForTest(
		t,
		first,
		"alice",
		"native-release-request-0004",
		"windows-laptop-01",
	)
	requests := []nativeworker.ReleaseRequest{
		{
			LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
			ExpectedRunRevision: lease.RunRevision,
			Reason:              nativeworker.ReleaseWorkerShutdown, Capability: lease.Capability,
		},
		{
			LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
			ExpectedRunRevision: lease.RunRevision,
			Reason:              nativeworker.ReleasePreparationFailed, Capability: lease.Capability,
		},
	}
	repositories := []*Repository{first, second}
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var ready sync.WaitGroup
	ready.Add(len(requests))
	for index := range requests {
		go func(repository *Repository, request nativeworker.ReleaseRequest) {
			ready.Done()
			<-start
			results <- repository.ReleaseNativeLease(
				context.Background(), "windows-laptop-01", request,
			)
		}(repositories[index], requests[index])
	}
	ready.Wait()
	close(start)

	succeeded := 0
	conflicted := 0
	for range requests {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, run.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent release error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("successful releases = %d, conflicts = %d", succeeded, conflicted)
	}
}

func TestNativeLeaseReleaseMigrationUpgrade(t *testing.T) {
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
	for _, name := range []string{
		"0001_initial.sql",
		"0002_reconciliation_leases.sql",
		"0003_kubernetes_executions.sql",
		"0004_baselines.sql",
		"0005_native_worker_presence.sql",
		"0006_run_execution_targets.sql",
		"0007_native_execution_leases.sql",
	} {
		document, err := migrations.ReadFile("migrations/" + name)
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
		`, name, hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	accepted := acceptNativeRun(t, repository, "alice", "native-release-upgrade-0001")
	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	plan, err := json.Marshal(postgresNativePlan(t))
	if err != nil {
		t.Fatal(err)
	}
	capability := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digest := sha256.Sum256([]byte(capability))
	if _, err = repository.db.Exec(`
		INSERT INTO perfeng_control.native_execution_leases(
			run_id,lease_id,attempt_id,worker_id,capability_sha256,
			issued_at,expires_at,renew_after_seconds,plan
		) VALUES (
			$1,'11111111-1111-4111-8111-111111111111',
			'22222222-2222-4222-8222-222222222222','windows-laptop-01',$2,
			clock_timestamp(),clock_timestamp()+interval '2 minutes',40,$3
		)
	`, accepted.Run.ID, digest[:], plan); err != nil {
		t.Fatal(err)
	}

	if err = repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	var releases int
	if err = repository.db.QueryRow(`
		SELECT count(*)
		FROM perfeng_control.native_execution_leases
		WHERE released_at IS NOT NULL OR release_reason IS NOT NULL OR release_message IS NOT NULL
	`).Scan(&releases); err != nil || releases != 0 {
		t.Fatal("upgrade changed active native lease authority", err)
	}
	if err = repository.ReleaseNativeLease(
		testContext,
		"windows-laptop-01",
		nativeworker.ReleaseRequest{
			LeaseID:             "11111111-1111-4111-8111-111111111111",
			AttemptID:           "22222222-2222-4222-8222-222222222222",
			ExpectedRunRevision: accepted.Run.Revision,
			Reason:              nativeworker.ReleaseWorkerShutdown, Capability: capability,
		},
	); err != nil {
		t.Fatal("upgraded lease could not be released", err)
	}
	if _, err = repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET release_reason='UNKNOWN'
		WHERE run_id=$1
	`, accepted.Run.ID); err == nil {
		t.Fatal("database accepted an unknown native release reason")
	}
}
