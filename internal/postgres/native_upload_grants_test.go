package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func nativeUploadRequest(lease nativeworker.Lease) nativeworker.UploadRequest {
	return nativeworker.UploadRequest{
		LeaseID: lease.LeaseID, AttemptID: lease.AttemptID,
		ExpectedRunRevision: lease.RunRevision,
		Slot:                "measurements",
		Size:                4096,
		SHA256:              strings.Repeat("f", 64),
		MediaType:           "application/json",
		Capability:          lease.Capability,
	}
}

func TestNativeUploadGrantPersistsExactRetryWithoutDeliverySecrets(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-upload-request-0001",
		"windows-laptop-01",
	)
	request := nativeUploadRequest(lease)
	first, err := repository.ReserveNativeUploadGrant(
		testContext,
		"windows-laptop-01",
		request,
		2*time.Minute,
	)
	if err != nil || !first.Valid() || first.RunID != accepted.Run.ID ||
		first.ObjectKey != "runs/"+accepted.Run.ID+"/attempts/"+lease.AttemptID+"/measurements" ||
		first.MaximumBytes != 1<<20 || first.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("reservation = %#v, error = %v", first, err)
	}
	second, err := repository.ReserveNativeUploadGrant(
		testContext,
		"windows-laptop-01",
		request,
		2*time.Minute,
	)
	if err != nil || second.GrantID != first.GrantID ||
		second.IssuedAt.Before(first.IssuedAt) || second.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("retry = %#v, error = %v", second, err)
	}

	var count int
	var checksum []byte
	if err = repository.db.QueryRow(`
		SELECT sha256
		FROM perfeng_control.native_upload_grants
		WHERE lease_id=$1 AND slot=$2
	`, lease.LeaseID, request.Slot).Scan(&checksum); err != nil ||
		hex.EncodeToString(checksum) != request.SHA256 {
		t.Fatal("upload declaration was not persisted exactly once", err)
	}
	if err = repository.db.QueryRow(`
		SELECT count(*)
		FROM perfeng_control.native_upload_grants
		WHERE lease_id=$1 AND slot=$2
	`, lease.LeaseID, request.Slot).Scan(&count); err != nil || count != 1 {
		t.Fatal("upload declaration was duplicated", err)
	}
	rows, err := repository.db.Query(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema='perfeng_control' AND table_name='native_upload_grants'
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
	var columns []string
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
		"grant_id", "run_id", "lease_id", "attempt_id", "slot", "object_key",
		"declared_size", "maximum_bytes", "sha256", "media_type", "issued_at", "expires_at",
	}
	if !reflect.DeepEqual(columns, wantColumns) {
		t.Fatalf("native upload grant columns = %v", columns)
	}
}

func TestNativeUploadGrantRejectsStaleMismatchedAndChangedDeclarations(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	_, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-upload-request-0002",
		"windows-laptop-01",
	)
	valid := nativeUploadRequest(lease)
	stale := valid
	stale.ExpectedRunRevision++
	if _, err := repository.ReserveNativeUploadGrant(
		testContext, "windows-laptop-01", stale, 2*time.Minute,
	); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	for name, change := range map[string]func(*nativeworker.UploadRequest) string{
		"worker": func(*nativeworker.UploadRequest) string { return "windows-laptop-02" },
		"lease": func(request *nativeworker.UploadRequest) string {
			request.LeaseID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"attempt": func(request *nativeworker.UploadRequest) string {
			request.AttemptID = "33333333-3333-4333-8333-333333333333"
			return "windows-laptop-01"
		},
		"capability": func(request *nativeworker.UploadRequest) string {
			request.Capability = strings.Repeat("e", 64)
			return "windows-laptop-01"
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			workerID := change(&request)
			if _, err := repository.ReserveNativeUploadGrant(
				testContext, workerID, request, 2*time.Minute,
			); !errors.Is(err, nativeworker.ErrLeaseGone) {
				t.Fatalf("authority error = %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*nativeworker.UploadRequest){
		"slot":       func(request *nativeworker.UploadRequest) { request.Slot = "trace" },
		"media type": func(request *nativeworker.UploadRequest) { request.MediaType = "application/x-ndjson" },
		"size":       func(request *nativeworker.UploadRequest) { request.Size = 1<<20 + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if _, err := repository.ReserveNativeUploadGrant(
				testContext, "windows-laptop-01", request, 2*time.Minute,
			); !errors.Is(err, nativeworker.ErrArtifactPlan) {
				t.Fatalf("artifact-plan error = %v", err)
			}
		})
	}
	if _, err := repository.ReserveNativeUploadGrant(
		testContext, "windows-laptop-01", valid, 2*time.Minute,
	); err != nil {
		t.Fatal(err)
	}
	changed := valid
	changed.SHA256 = strings.Repeat("d", 64)
	if _, err := repository.ReserveNativeUploadGrant(
		testContext, "windows-laptop-01", changed, 2*time.Minute,
	); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("changed declaration error = %v", err)
	}
}

func TestNativeUploadGrantRejectsExpiredAndReleasedAuthority(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	accepted, lease := acquireNativeLeaseForTest(
		t,
		repository,
		"alice",
		"native-upload-request-0003",
		"windows-laptop-01",
	)
	request := nativeUploadRequest(lease)
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.native_execution_leases
		SET issued_at=clock_timestamp()-interval '3 minutes',
			expires_at=clock_timestamp()-interval '1 minute'
		WHERE run_id=$1
	`, accepted.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReserveNativeUploadGrant(
		testContext, "windows-laptop-01", request, 2*time.Minute,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired lease error = %v", err)
	}

	_, replacement := acquireNativeLeaseForTest(
		t,
		repository,
		"bob",
		"native-upload-request-0004",
		"windows-laptop-02",
	)
	release := nativeworker.ReleaseRequest{
		LeaseID: replacement.LeaseID, AttemptID: replacement.AttemptID,
		ExpectedRunRevision: replacement.RunRevision,
		Reason:              nativeworker.ReleaseWorkerShutdown,
		Capability:          replacement.Capability,
	}
	if err := repository.ReleaseNativeLease(
		testContext, "windows-laptop-02", release,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReserveNativeUploadGrant(
		testContext, "windows-laptop-02", nativeUploadRequest(replacement), 2*time.Minute,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("released lease error = %v", err)
	}
}

func TestConcurrentNativeUploadGrantRetryHasOneIdentity(t *testing.T) {
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
		"native-upload-request-0005",
		"windows-laptop-01",
	)
	request := nativeUploadRequest(lease)
	repositories := []*Repository{first, second}
	start := make(chan struct{})
	type outcome struct {
		reservation nativeworker.UploadReservation
		err         error
	}
	results := make(chan outcome, len(repositories))
	var ready sync.WaitGroup
	ready.Add(len(repositories))
	for _, repository := range repositories {
		go func(repository *Repository) {
			ready.Done()
			<-start
			reservation, err := repository.ReserveNativeUploadGrant(
				testContext,
				"windows-laptop-01",
				request,
				2*time.Minute,
			)
			results <- outcome{reservation: reservation, err: err}
		}(repository)
	}
	ready.Wait()
	close(start)

	var grantID string
	for range repositories {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if grantID != "" && result.reservation.GrantID != grantID {
			t.Fatal("concurrent identical retry created another grant identity")
		}
		grantID = result.reservation.GrantID
	}
	var count int
	if err := first.db.QueryRow(`
		SELECT count(*) FROM perfeng_control.native_upload_grants
		WHERE lease_id=$1 AND slot=$2
	`, lease.LeaseID, request.Slot).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent retry persisted duplicate grants", err)
	}
}

func TestNativeUploadGrantMigrationUpgrade(t *testing.T) {
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
	accepted := acceptNativeRun(t, repository, "alice", "native-upload-upgrade-0001")
	recordEligibleNativeWorker(t, repository, "windows-laptop-01")
	lease := insertOldStyleNativeLease(t, repository, accepted, "windows-laptop-01")
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	reservation, err := repository.ReserveNativeUploadGrant(
		testContext,
		"windows-laptop-01",
		nativeUploadRequest(lease),
		2*time.Minute,
	)
	if err != nil || !reservation.Valid() {
		t.Fatal("upgraded native lease could not reserve an upload grant", err)
	}
	if _, err = repository.db.Exec(`
		UPDATE perfeng_control.native_upload_grants
		SET object_key='runs/worker-selected'
		WHERE grant_id=$1
	`, reservation.GrantID); err == nil {
		t.Fatal("database accepted a noncanonical native upload object key")
	}
}
