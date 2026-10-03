package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeTerminalArtifactPlanSelectsProtocol(t *testing.T) {
	baseline, err := json.Marshal(pinnedNativeV2Plan(t))
	if err != nil {
		t.Fatal(err)
	}
	slots, err := nativeTerminalArtifactPlan(baseline, nativeLeaseProtocolV2)
	if err != nil || len(slots) != 2 || slots[0].Slot != "measurements" ||
		slots[1].Slot != "manifest" {
		t.Fatalf("v2 terminal plan = %#v, err=%v", slots, err)
	}
	if _, err := nativeTerminalArtifactPlan(baseline, nativeLeaseProtocolV1); err == nil {
		t.Fatal("v2 baseline plan was decoded as a v1 assignment")
	}
}

func registerNativeV2SlotForTest(
	t *testing.T,
	fixture nativeV2LeaseFixture,
	revision int64,
	slot string,
) nativeworker.ArtifactReference {
	t.Helper()
	request := v2UploadRequest(fixture, slot)
	request.ExpectedRunRevision = revision
	if slot == "manifest" {
		request.Size = 2048
		request.SHA256 = strings.Repeat("e", 64)
	}
	reservation, err := fixture.repository.ReserveNativeBaselineUploadGrant(
		testContext, fixture.workerID, request, 2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	registration := v2ArtifactRequest(request, reservation)
	candidate, err := fixture.repository.PrepareNativeBaselineArtifact(
		testContext, fixture.workerID, registration,
	)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := fixture.repository.RegisterNativeBaselineArtifact(
		testContext, fixture.workerID, registration,
		nativeworker.VerifiedArtifact{
			Candidate: candidate, URI: "s3://perfeng-artifacts/" + reservation.ObjectKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	return reference
}

func v2CompletedTerminalRequest(
	fixture nativeV2LeaseFixture,
	revision int64,
	manifest nativeworker.ArtifactReference,
) nativeworker.TerminalRequest {
	return nativeworker.TerminalRequest{
		Kind:    nativeworker.TerminalCompleted,
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: revision, ManifestArtifactID: manifest.ArtifactID,
		Capability: fixture.authority.capability,
	}
}

func TestNativeV2TerminalCompletionFencesEvidenceAndExactRetry(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	running, err := repository.RenewNativeBaselineLease(
		testContext, fixture.workerID,
		v2RenewalRequest(fixture, fixture.accepted.Run.Revision, nativeworker.PhaseRunning),
		2*time.Minute, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	registerNativeV2SlotForTest(t, fixture, running.RunRevision, "measurements")
	manifest := registerNativeV2SlotForTest(t, fixture, running.RunRevision, "manifest")
	request := v2CompletedTerminalRequest(fixture, running.RunRevision, manifest)
	if _, err := repository.ReportNativeTerminal(
		testContext, fixture.workerID, request,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("v1 terminal accepted v2 lease: %v", err)
	}
	first, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, request,
	)
	if err != nil || !first.Valid() || first.Disposition != nativeworker.TerminalAccepted ||
		first.RunRevision <= running.RunRevision {
		t.Fatalf("v2 terminal acknowledgement = %#v, err=%v", first, err)
	}
	current, err := repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current.State != run.StateCollecting || current.Revision != first.RunRevision {
		t.Fatalf("completed v2 Run = %#v, err=%v", current, err)
	}
	retry, err := repository.ReportNativeBaselineTerminal(testContext, fixture.workerID, request)
	if err != nil || retry.Disposition != nativeworker.TerminalAlreadyAccepted ||
		retry.RunRevision != first.RunRevision {
		t.Fatalf("v2 terminal retry = %#v, err=%v", retry, err)
	}
	conflicting := request
	conflicting.ManifestArtifactID = "44444444-4444-4444-8444-444444444444"
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, conflicting,
	); !errors.Is(err, nativeworker.ErrTerminalConflict) {
		t.Fatalf("changed v2 outcome = %v", err)
	}
	var version int
	var v1Worker *string
	var v2Worker string
	if err := repository.db.QueryRow(`
		SELECT protocol_version,worker_v1_id,worker_v2_id
		FROM perfeng_control.native_terminal_reports WHERE run_id=$1
	`, fixture.accepted.Run.ID).Scan(&version, &v1Worker, &v2Worker); err != nil ||
		version != nativeLeaseProtocolV2 || v1Worker != nil || v2Worker != fixture.workerID {
		t.Fatalf("v2 terminal worker identity = (%d, %v, %q), err=%v", version, v1Worker, v2Worker, err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_terminal_reports
		SET worker_id='missing-v2-worker' WHERE run_id=$1
	`, fixture.accepted.Run.ID); err == nil {
		t.Fatal("v2 terminal report lost its worker foreign key")
	}
}

func TestNativeV2TerminalRejectsMissingAndOutOfOrderEvidence(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	revision := fixture.accepted.Run.Revision
	manifest := registerNativeV2SlotForTest(t, fixture, revision, "manifest")
	request := v2CompletedTerminalRequest(fixture, revision, manifest)
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, request,
	); !errors.Is(err, nativeworker.ErrTerminalEvidence) {
		t.Fatalf("missing v2 measurements = %v", err)
	}
	measurements := registerNativeV2SlotForTest(t, fixture, revision, "measurements")
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_artifacts
		SET registered_at=(SELECT registered_at FROM perfeng_control.native_artifacts WHERE artifact_id=$2)
			+ interval '1 second'
		WHERE artifact_id=$1
	`, measurements.ArtifactID, manifest.ArtifactID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, request,
	); !errors.Is(err, nativeworker.ErrTerminalEvidence) {
		t.Fatalf("v2 manifest registered before measurements = %v", err)
	}
}

func TestNativeV2TerminalFailureAndAuthorityIsolation(t *testing.T) {
	fixture := newNativeV2LeaseFixture(t)
	repository := fixture.repository
	request := nativeworker.TerminalRequest{
		Kind:    nativeworker.TerminalFailed,
		LeaseID: fixture.authority.leaseID, AttemptID: fixture.authority.attemptID,
		ExpectedRunRevision: fixture.accepted.Run.Revision,
		Failure: &nativeworker.TerminalFailure{
			Code: nativeworker.TerminalFailureTool, Message: "reviewed runner failed", Retryable: false,
		},
		Capability: fixture.authority.capability,
	}
	stale := request
	stale.ExpectedRunRevision++
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, stale,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale v2 terminal = %v", err)
	}
	wrong := request
	wrong.Capability = strings.Repeat("f", 64)
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, fixture.workerID, wrong,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("wrong v2 capability = %v", err)
	}
	first, err := repository.ReportNativeBaselineTerminal(testContext, fixture.workerID, request)
	if err != nil || first.Disposition != nativeworker.TerminalAccepted {
		t.Fatalf("v2 failure acknowledgement = %#v, err=%v", first, err)
	}
	current, err := repository.Get(testContext, "alice", fixture.accepted.Run.ID)
	if err != nil || current.State != run.StateTestFailure || current.Failure == nil ||
		current.Failure.Code != run.FailureCodeToolError ||
		current.Failure.Message == request.Failure.Message {
		t.Fatalf("v2 failure Run = %#v, err=%v", current, err)
	}
	retry, err := repository.ReportNativeBaselineTerminal(testContext, fixture.workerID, request)
	if err != nil || retry.Disposition != nativeworker.TerminalAlreadyAccepted ||
		retry.RunRevision != first.RunRevision {
		t.Fatalf("v2 failure retry = %#v, err=%v", retry, err)
	}
}

func TestNativeV2TerminalRejectsV1Lease(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, lease := acquireNativeLeaseForTest(
		t, repository, "alice", "native-v2-terminal-isolation-0001", "windows-laptop-01",
	)
	request := nativeworker.TerminalRequest{
		Kind: nativeworker.TerminalFailed, LeaseID: lease.LeaseID,
		AttemptID: lease.AttemptID, ExpectedRunRevision: lease.RunRevision,
		Failure: &nativeworker.TerminalFailure{
			Code: nativeworker.TerminalFailureUpload, Message: "upload failed",
		},
		Capability: lease.Capability,
	}
	if _, err := repository.ReportNativeBaselineTerminal(
		testContext, "windows-laptop-01", request,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("v2 terminal accepted v1 lease: %v", err)
	}
}

func TestNativeTerminalProtocolUpgradePreservesV1Reports(t *testing.T) {
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
		if entry.Name() == "0015_native_terminal_protocol_version.sql" {
			break
		}
		document, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
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
	_, lease := acquireNativeLeaseForTest(
		t, repository, "alice", "native-terminal-version-upgrade-0001", "windows-laptop-01",
	)
	if _, err := repository.db.Exec(`
		INSERT INTO perfeng_control.native_terminal_reports(
			run_id,lease_id,attempt_id,worker_id,expected_run_revision,kind,
			failure_code,failure_message,failure_retryable,resulting_run_revision,accepted_at
		) VALUES ($1,$2,$3,$4,$5,'FAILED','UPLOAD_FAILED','old v1 failure',false,$6,clock_timestamp())
	`, lease.RunID, lease.LeaseID, lease.AttemptID, "windows-laptop-01",
		lease.RunRevision, lease.RunRevision+1); err != nil {
		t.Fatal(err)
	}
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	var version int
	var v1Worker string
	var v2Worker *string
	if err := repository.db.QueryRow(`
		SELECT protocol_version,worker_v1_id,worker_v2_id
		FROM perfeng_control.native_terminal_reports WHERE run_id=$1
	`, lease.RunID).Scan(&version, &v1Worker, &v2Worker); err != nil ||
		version != nativeLeaseProtocolV1 || v1Worker != "windows-laptop-01" || v2Worker != nil {
		t.Fatalf("upgraded v1 terminal identity = (%d, %q, %v), err=%v", version, v1Worker, v2Worker, err)
	}
	_, second := acquireNativeLeaseForTest(
		t, repository, "alice", "native-terminal-version-upgrade-0002", "windows-laptop-02",
	)
	if _, err := repository.db.Exec(`
		INSERT INTO perfeng_control.native_terminal_reports(
			run_id,lease_id,attempt_id,worker_id,expected_run_revision,kind,
			failure_code,failure_message,failure_retryable,resulting_run_revision,accepted_at
		) VALUES ($1,$2,$3,$4,$5,'FAILED','UPLOAD_FAILED','old v1 writer',false,$6,clock_timestamp())
	`, second.RunID, second.LeaseID, second.AttemptID, "windows-laptop-02",
		second.RunRevision, second.RunRevision+1); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.QueryRow(`
		SELECT protocol_version FROM perfeng_control.native_terminal_reports WHERE run_id=$1
	`, second.RunID).Scan(&version); err != nil || version != nativeLeaseProtocolV1 {
		t.Fatalf("old-style v1 terminal writer version = %d, err=%v", version, err)
	}
}
