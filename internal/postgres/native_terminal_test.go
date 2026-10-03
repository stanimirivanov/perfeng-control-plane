package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func registerNativeSlotForTest(
	t *testing.T,
	repository *Repository,
	workerID string,
	lease nativeworker.Lease,
	slot string,
) nativeworker.ArtifactReference {
	t.Helper()
	upload := nativeUploadRequest(lease)
	upload.Slot = slot
	if slot == "manifest" {
		upload.Size = 2048
		upload.SHA256 = strings.Repeat("e", 64)
	}
	reservation, err := repository.ReserveNativeUploadGrant(
		testContext,
		workerID,
		upload,
		2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeArtifactRegistrationRequest(lease, reservation)
	candidate, err := repository.PrepareNativeArtifact(testContext, workerID, request)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := repository.RegisterNativeArtifact(
		testContext,
		workerID,
		request,
		nativeworker.VerifiedArtifact{
			Candidate: candidate,
			URI:       "s3://perfeng-artifacts/" + candidate.Reservation.ObjectKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return reference
}

func completedNativeTerminalRequest(
	lease nativeworker.Lease,
	manifest nativeworker.ArtifactReference,
) nativeworker.TerminalRequest {
	return nativeworker.TerminalRequest{
		Kind: nativeworker.TerminalCompleted, LeaseID: lease.LeaseID,
		AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
		ManifestArtifactID: manifest.ArtifactID, Capability: lease.Capability,
	}
}

func TestNativeTerminalCompletionPersistsExactRetryAndExpiresLease(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-terminal-complete-0001",
		"windows-laptop-01",
	)
	renewal, err := repository.RenewNativeLease(
		testContext,
		"windows-laptop-01",
		nativeworker.RenewalRequest{
			LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
			ExpectedRunRevision: lease.RunRevision, Phase: nativeworker.PhaseRunning,
			Capability: lease.Capability,
		},
		2*time.Minute,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.RunRevision = renewal.RunRevision
	registerNativeSlotForTest(t, repository, "windows-laptop-01", lease, "measurements")
	manifest := registerNativeSlotForTest(t, repository, "windows-laptop-01", lease, "manifest")
	request := completedNativeTerminalRequest(lease, manifest)

	first, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		request,
	)
	if err != nil || first.AttemptID != lease.AttemptID ||
		first.Disposition != nativeworker.TerminalAccepted || first.RunRevision != 6 {
		t.Fatalf("first acknowledgement = %#v, error = %v", first, err)
	}
	current, err := repository.Get(testContext, "alice", accepted.Run.ID)
	if err != nil || current.State != run.StateCollecting || current.Revision != first.RunRevision {
		t.Fatalf("completed native Run = %#v, error = %v", current, err)
	}
	second, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		request,
	)
	if err != nil || second.AttemptID != first.AttemptID ||
		second.RunRevision != first.RunRevision ||
		second.Disposition != nativeworker.TerminalAlreadyAccepted {
		t.Fatalf("retry acknowledgement = %#v, error = %v", second, err)
	}
	conflicting := request
	conflicting.ManifestArtifactID = "44444444-4444-4444-8444-444444444444"
	if _, err = repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		conflicting,
	); !errors.Is(err, nativeworker.ErrTerminalConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}

	var active bool
	var reports int
	if err = repository.db.QueryRow(`
		SELECT expires_at>clock_timestamp()
		FROM perfeng_control.native_execution_leases
		WHERE run_id=$1
	`, accepted.Run.ID).Scan(&active); err != nil || active {
		t.Fatal("accepted terminal report retained an active lease", err)
	}
	if err = repository.db.QueryRow(`
		SELECT count(*)
		FROM perfeng_control.native_terminal_reports
		WHERE run_id=$1 AND manifest_artifact_id=$2
	`, accepted.Run.ID, manifest.ArtifactID).Scan(&reports); err != nil || reports != 1 {
		t.Fatal("terminal report was not persisted exactly once", err)
	}
}

func TestNativeTerminalCompletionRequiresCompleteOrderedEvidence(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-terminal-evidence-0001",
		"windows-laptop-01",
	)
	manifest := registerNativeSlotForTest(t, repository, "windows-laptop-01", lease, "manifest")
	request := completedNativeTerminalRequest(lease, manifest)
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		request,
	); !errors.Is(err, nativeworker.ErrTerminalEvidence) {
		t.Fatalf("incomplete evidence error = %v", err)
	}

	registerNativeSlotForTest(t, repository, "windows-laptop-01", lease, "measurements")
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		request,
	); !errors.Is(err, nativeworker.ErrTerminalEvidence) {
		t.Fatalf("manifest-not-last error = %v", err)
	}
}

func TestNativeTerminalFailureMapsClassificationsAndPreservesReport(t *testing.T) {
	for _, test := range []struct {
		name         string
		code         nativeworker.TerminalFailureCode
		wantState    run.State
		wantRunCode  run.FailureCode
		wantRevision int64
	}{
		{"tool", nativeworker.TerminalFailureTool, run.StateTestFailure, run.FailureCodeToolError, 7},
		{"capture", nativeworker.TerminalFailureCapture, run.StateInfrastructureFailure, run.FailureCodeInfrastructureError, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := openTest(t, testDatabase(t))
			if err := repository.Migrate(testContext); err != nil {
				t.Fatal(err)
			}
			accepted, lease := acquireNativeLeaseForTest(
				t,
				repository,
				"alice",
				"native-terminal-failed-"+test.name,
				"windows-laptop-01",
			)
			request := nativeworker.TerminalRequest{
				Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
				AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
				Failure: &nativeworker.TerminalFailure{
					Code: test.code, Message: "safe worker explanation", Retryable: true,
				},
				Capability: lease.Capability,
			}
			acknowledgement, err := repository.ReportNativeTerminal(
				testContext,
				"windows-laptop-01",
				request,
			)
			if err != nil || acknowledgement.RunRevision != test.wantRevision ||
				acknowledgement.Disposition != nativeworker.TerminalAccepted {
				t.Fatalf("acknowledgement = %#v, error = %v", acknowledgement, err)
			}
			current, err := repository.Get(testContext, "alice", accepted.Run.ID)
			if err != nil || current.State != test.wantState || current.Failure == nil ||
				current.Failure.Code != test.wantRunCode ||
				current.Failure.Message == request.Failure.Message {
				t.Fatalf("failed native Run = %#v, error = %v", current, err)
			}
			var message string
			var retryable bool
			if err = repository.db.QueryRow(`
				SELECT failure_message,failure_retryable
				FROM perfeng_control.native_terminal_reports
				WHERE run_id=$1
			`, accepted.Run.ID).Scan(&message, &retryable); err != nil ||
				message != request.Failure.Message || !retryable {
				t.Fatal("exact terminal failure was not preserved", err)
			}
		})
	}
}

func TestNativeTerminalRejectsStaleExpiredAndMismatchedAuthority(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-terminal-authority-0001",
		"windows-laptop-01",
	)
	valid := nativeworker.TerminalRequest{
		Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
		AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
		Failure: &nativeworker.TerminalFailure{
			Code: nativeworker.TerminalFailureUpload, Message: "upload failed",
		},
		Capability: lease.Capability,
	}
	stale := valid
	stale.ExpectedRunRevision++
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		stale,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	mismatched := valid
	mismatched.Capability = strings.Repeat("f", 64)
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		mismatched,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("capability mismatch error = %v", err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 minute'
		WHERE run_id=$1
	`, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		valid,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired lease error = %v", err)
	}
}

func TestConcurrentNativeTerminalReportsPreserveOneOutcome(t *testing.T) {
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
		"native-terminal-concurrent-0001",
		"windows-laptop-01",
	)
	requests := []nativeworker.TerminalRequest{
		{
			Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
			AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
			Failure: &nativeworker.TerminalFailure{
				Code: nativeworker.TerminalFailureTool, Message: "tool failed",
			},
			Capability: lease.Capability,
		},
		{
			Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
			AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
			Failure: &nativeworker.TerminalFailure{
				Code: nativeworker.TerminalFailureCapture, Message: "capture failed",
			},
			Capability: lease.Capability,
		},
	}
	repositories := []*Repository{first, second}
	start := make(chan struct{})
	results := make(chan error, len(requests))
	var ready sync.WaitGroup
	ready.Add(len(requests))
	for index := range requests {
		go func(repository *Repository, request nativeworker.TerminalRequest) {
			ready.Done()
			<-start
			_, err := repository.ReportNativeTerminal(
				testContext,
				"windows-laptop-01",
				request,
			)
			results <- err
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
		case errors.Is(err, nativeworker.ErrTerminalConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent terminal error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("successful terminal reports = %d, conflicts = %d", succeeded, conflicted)
	}
}

func TestNativeTerminalMigrationUpgrade(t *testing.T) {
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
		"0008_native_lease_releases.sql",
		"0009_native_upload_grants.sql",
		"0010_native_artifacts.sql",
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
	accepted := acceptNativeRun(t, repository, "alice", "native-terminal-upgrade-0001")
	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	lease := insertOldStyleNativeLease(t, repository, accepted, "windows-laptop-01")
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	request := nativeworker.TerminalRequest{
		Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
		AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
		Failure: &nativeworker.TerminalFailure{
			Code:    nativeworker.TerminalFailureHostUnqualified,
			Message: "host qualification changed",
		},
		Capability: lease.Capability,
	}
	if _, err := repository.ReportNativeTerminal(
		testContext,
		"windows-laptop-01",
		request,
	); err != nil {
		t.Fatal("upgraded database could not accept terminal report", err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_terminal_reports
		SET failure_code='UNKNOWN'
	`); err == nil {
		t.Fatal("database accepted unknown terminal failure code")
	}
}
