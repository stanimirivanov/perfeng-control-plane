package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func nativeArtifactRegistrationRequest(
	lease nativeworker.Lease,
	reservation nativeworker.UploadReservation,
) nativeworker.ArtifactRegistrationRequest {
	return nativeworker.ArtifactRegistrationRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision, GrantID: reservation.GrantID,
		Slot: reservation.Slot, Size: reservation.Size, SHA256: reservation.SHA256,
		Capability: lease.Capability,
	}
}

func reserveNativeArtifactForTest(
	t *testing.T,
	repository *Repository,
	principal string,
	idempotencyKey string,
	workerID string,
) (nativeworker.Lease, nativeworker.ArtifactRegistrationRequest) {
	t.Helper()
	_, lease := acquireNativeLeaseForTest(t, repository, principal, idempotencyKey, workerID)
	reservation, err := repository.ReserveNativeUploadGrant(
		testContext,
		workerID,
		nativeUploadRequest(lease),
		2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	return lease, nativeArtifactRegistrationRequest(lease, reservation)
}

func TestNativeArtifactRegistrationPersistsVerifiedExactRetry(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	lease, request := reserveNativeArtifactForTest(
		t,
		repository,
		"alice",
		"native-artifact-register-0001",
		"windows-laptop-01",
	)
	candidate, err := repository.PrepareNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
	)
	if err != nil || !candidate.Valid() || candidate.Kind != "measurements" ||
		candidate.Format != "playwright-measurements-v2" {
		t.Fatalf("candidate = %#v, error = %v", candidate, err)
	}
	verified := nativeworker.VerifiedArtifact{
		Candidate: candidate,
		URI:       "s3://perfeng-artifacts/" + candidate.Reservation.ObjectKey,
	}
	first, err := repository.RegisterNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
		verified,
	)
	if err != nil || !first.Valid() || first.RunID != lease.RunID ||
		first.AttemptID != lease.AttemptID || first.URI != verified.URI {
		t.Fatalf("reference = %#v, error = %v", first, err)
	}
	second, err := repository.RegisterNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
		verified,
	)
	if err != nil || second != first {
		t.Fatalf("retry = %#v, error = %v", second, err)
	}

	var count int
	if err = repository.db.QueryRow(`
		SELECT count(*)
		FROM perfeng_control.native_artifacts
		WHERE grant_id=$1
	`, request.GrantID).Scan(&count); err != nil || count != 1 {
		t.Fatal("native artifact was not persisted exactly once", err)
	}
	rows, err := repository.db.Query(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema='perfeng_control' AND table_name='native_artifacts'
		ORDER BY ordinal_position
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	columns := make([]string, 0, 6)
	for rows.Next() {
		var column string
		if err = rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantColumns := []string{
		"artifact_id", "grant_id", "uri", "kind", "format", "registered_at",
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("native artifact columns = %v", columns)
	}
}

func TestNativeArtifactRegistrationRechecksAuthorityAfterVerification(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, request := reserveNativeArtifactForTest(
		t,
		repository,
		"alice",
		"native-artifact-register-0002",
		"windows-laptop-01",
	)
	candidate, err := repository.PrepareNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	verified := nativeworker.VerifiedArtifact{
		Candidate: candidate,
		URI:       "s3://perfeng-artifacts/" + candidate.Reservation.ObjectKey,
	}
	if _, err = repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 minute'
		WHERE lease_id=$1
	`, request.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.RegisterNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
		verified,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired lease error = %v", err)
	}
}

func TestNativeArtifactRegistrationRejectsStaleAndChangedGrantFacts(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, valid := reserveNativeArtifactForTest(
		t,
		repository,
		"alice",
		"native-artifact-register-0003",
		"windows-laptop-01",
	)
	stale := valid
	stale.ExpectedRunRevision++
	if _, err := repository.PrepareNativeArtifact(
		testContext,
		"windows-laptop-01",
		stale,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	for name, mutate := range map[string]func(*nativeworker.ArtifactRegistrationRequest){
		"grant": func(request *nativeworker.ArtifactRegistrationRequest) {
			request.GrantID = "55555555-5555-4555-8555-555555555555"
		},
		"slot": func(request *nativeworker.ArtifactRegistrationRequest) {
			request.Slot = "manifest"
		},
		"size": func(request *nativeworker.ArtifactRegistrationRequest) {
			request.Size++
		},
		"checksum": func(request *nativeworker.ArtifactRegistrationRequest) {
			request.SHA256 = strings.Repeat("d", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if _, err := repository.PrepareNativeArtifact(
				testContext,
				"windows-laptop-01",
				request,
			); !errors.Is(err, nativeworker.ErrUploadGrantMismatch) {
				t.Fatalf("grant mismatch error = %v", err)
			}
		})
	}
}

func TestNativeArtifactMigrationUpgrade(t *testing.T) {
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
	_, request := reserveNativeArtifactForTest(
		t,
		repository,
		"alice",
		"native-artifact-upgrade-0001",
		"windows-laptop-01",
	)
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	candidate, err := repository.PrepareNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	verified := nativeworker.VerifiedArtifact{
		Candidate: candidate,
		URI:       "s3://perfeng-artifacts/" + candidate.Reservation.ObjectKey,
	}
	if _, err = repository.RegisterNativeArtifact(
		testContext,
		"windows-laptop-01",
		request,
		verified,
	); err != nil {
		t.Fatal("upgraded database could not register a native artifact", err)
	}
}
