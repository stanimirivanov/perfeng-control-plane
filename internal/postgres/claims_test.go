package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func claimsDB(t *testing.T) (*Repository, string) {
	t.Helper()
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}

	return r, dsn
}
func claimBatch(t *testing.T, r *Repository, worker string, limit int) []run.Claim {
	t.Helper()
	claims, err := r.ClaimRuns(testContext, worker, limit, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	return claims
}
func oneClaim(t *testing.T, r *Repository, worker string) run.Claim {
	t.Helper()
	claims := claimBatch(t, r, worker, 1)
	if len(claims) != 1 {
		t.Fatalf("expected one claim, got %d", len(claims))
	}

	return claims[0]
}

func TestClaimsLifecycle(t *testing.T) {
	r, dsn := claimsDB(t)
	other := openTest(t, dsn)
	a := accepted(t, r, "alice", "request-key-claims")
	claim := oneClaim(t, r, "worker-1")
	if claim.Run != a.Run || claim.Lease.Principal != "alice" || !claim.Lease.Valid() {
		t.Fatal("invalid claim identity/snapshot")
	}
	if len(claimBatch(t, other, "worker-2", 100)) != 0 {
		t.Fatal("live lease stolen")
	}
	for _, alter := range []func(*run.Lease){
		func(l *run.Lease) { l.Principal = "bob" }, func(l *run.Lease) { l.WorkerID = "worker-2" },
		func(l *run.Lease) { l.Token = strings.Repeat("0", 32) },
	} {
		forged := claim.Lease
		alter(&forged)
		if _, err := other.RenewClaim(testContext, forged, time.Minute); !errors.Is(err, run.ErrLeaseLost) {
			t.Fatal("wrong owner accepted", err)
		}
	}
	// Caller expiry is not authority; renewal checks the persisted deadline.
	claim.Lease.ExpiresAt = time.Time{}
	renewed, err := other.RenewClaim(testContext, claim.Lease, 2*time.Minute)
	if err != nil || renewed.Run.Revision != 1 || renewed.Lease.Token != claim.Lease.Token {
		t.Fatal("renew changed run/token", err)
	}
	current, err := r.AdvanceClaim(testContext, renewed.Lease, 1, run.Change{State: "VALIDATING"})
	if err != nil || current.Revision != 2 {
		t.Fatal(err)
	}
	replay := accepted(t, other, "alice", "request-key-claims")
	if replay != a {
		t.Fatal("worker claim changed acceptance replay")
	}
	cancelled, err := other.Cancel(testContext, "alice", a.Run.ID)
	if err != nil || cancelled.State != "ABORTED" || cancelled.FinishedAt == nil {
		t.Fatal(err)
	}
	if _, err := r.AdvanceClaim(testContext, renewed.Lease, 2, run.Change{State: "PROVISIONING"}); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("stale worker overwrote cancellation", err)
	}
	if _, err := other.RenewClaim(testContext, renewed.Lease, time.Minute); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("terminal lease renewed", err)
	}
	if len(claimBatch(t, other, "worker-2", 100)) != 0 {
		t.Fatal("terminal run claimed")
	}
}

func TestClaimExpiryReleaseAndCancellation(t *testing.T) {
	r, _ := claimsDB(t)
	a := accepted(t, r, "alice", "request-key-expire")
	old := oneClaim(t, r, "same-worker")
	current, err := r.AdvanceClaim(testContext, old.Lease, old.Run.Revision, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	current, err = r.AdvanceClaim(testContext, old.Lease, current.Revision, run.Change{State: run.StateProvisioning})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseClaim(testContext, old.Lease, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(claimBatch(t, r, "other-worker", 10)) != 0 {
		t.Fatal("retry delay ignored")
	}
	if _, err := r.RenewClaim(testContext, old.Lease, time.Minute); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("released claim renewed", err)
	}
	if _, err := r.Cancel(testContext, "alice", a.Run.ID); err != nil {
		t.Fatal(err)
	}
	fresh := oneClaim(t, r, "same-worker")
	if fresh.Run.State != "CANCELLING" || fresh.Lease.Token == old.Lease.Token {
		t.Fatal("cancellation did not bypass retry delay with a new token")
	}
	if err := r.ReleaseClaim(testContext, old.Lease, 0); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("old owner released new lease", err)
	}
	if _, err := r.db.Exec("UPDATE perfeng_control.reconciliation_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE run_id=$1", fresh.Run.ID); err != nil {
		t.Fatal(err)
	}
	fresh.Lease.ExpiresAt = time.Now().Add(24 * time.Hour)
	if _, err := r.AdvanceClaim(testContext, fresh.Lease, fresh.Run.Revision, run.Change{State: "ABORTED"}); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("caller expiry bypassed stored deadline", err)
	}
	newest := oneClaim(t, r, "same-worker")
	if newest.Lease.Token == fresh.Lease.Token {
		t.Fatal("reclaim reused token")
	}
	if _, err := r.AdvanceClaim(testContext, fresh.Lease, fresh.Run.Revision, run.Change{State: "ABORTED"}); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("stale token accepted after reclaim", err)
	}
	if err := r.ReleaseClaim(testContext, newest.Lease, 0); err != nil {
		t.Fatal(err)
	}
	if oneClaim(t, r, "worker-3").Run.Revision != newest.Run.Revision {
		t.Fatal("coordination changed run revision")
	}
}

func TestConcurrentClaimsAndSkipLocked(t *testing.T) {
	r, dsn := claimsDB(t)
	other := openTest(t, dsn)
	for i := range 24 {
		accepted(t, r, fmt.Sprintf("principal-%02d", i), fmt.Sprintf("request-key-%04d", i))
	}
	var wg sync.WaitGroup
	results := make(chan run.Claim, 32)
	for i := range 8 {
		repository := r
		if i%2 == 1 {
			repository = other
		}
		wg.Go(func() {
			claims, err := repository.ClaimRuns(testContext, fmt.Sprintf("worker-%d", i), 4, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			for _, claim := range claims {
				results <- claim
			}
		})
	}
	wg.Wait()
	close(results)
	seen := make(map[string]run.Claim)
	for claim := range results {
		if _, ok := seen[claim.Run.ID]; ok {
			t.Fatal("run concurrently claimed twice")
		}
		seen[claim.Run.ID] = claim
	}
	// Short batches under contention are legal; a later poll finds remaining work.
	for _, claim := range claimBatch(t, r, "drain-worker", 100) {
		if _, ok := seen[claim.Run.ID]; ok {
			t.Fatal("active lease reclaimed")
		}
		seen[claim.Run.ID] = claim
	}
	if len(seen) != 24 {
		t.Fatalf("discovery lost work: %d", len(seen))
	}
	var lockedID string
	for _, claim := range seen {
		lockedID = claim.Run.ID
		if err := r.ReleaseClaim(testContext, claim.Lease, 0); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := r.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("SELECT run_id FROM perfeng_control.runs WHERE run_id=$1 FOR UPDATE", lockedID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testContext, 2*time.Second)
	defer cancel()
	claims, err := other.ClaimRuns(ctx, "skip-worker", 100, time.Minute)
	if err != nil || len(claims) != 23 {
		t.Fatal("locked row blocked unrelated claims", err, len(claims))
	}
	for _, claim := range claims {
		if claim.Run.ID == lockedID {
			t.Fatal("locked run claimed")
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if oneClaim(t, r, "last-worker").Run.ID != lockedID {
		t.Fatal("unlocked work was lost")
	}
}

func TestClaimsSurviveProcessRestart(t *testing.T) {
	r, dsn := claimsDB(t)
	accepted(t, r, "restart", "request-key-restart")
	old := oneClaim(t, r, "restart-worker")
	execution := executionFor(old.Run.ID)
	if err := r.BindExecution(testContext, old.Lease, execution); err != nil {
		t.Fatal(err)
	}
	checkChild := func(mode string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(testContext, 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaimsRestartChild$")
		command.Env = append(os.Environ(), "PERFENG_CLAIM_CHILD_DSN="+dsn, "PERFENG_CLAIM_CHILD_MODE="+mode,
			"PERFENG_CLAIM_CHILD_ID="+old.Run.ID, "PERFENG_CLAIM_CHILD_TOKEN="+old.Lease.Token)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("worker restart check failed: %v\n%s", err, output)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	checkChild("active")
	r = openTest(t, dsn)
	if _, err := r.db.Exec("UPDATE perfeng_control.reconciliation_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE run_id=$1", old.Run.ID); err != nil {
		t.Fatal(err)
	}
	checkChild("expired")
	if _, err := r.AdvanceClaim(testContext, old.Lease, 1, run.Change{State: "VALIDATING"}); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("old process could still write", err)
	}
}

func TestClaimsRestartChild(t *testing.T) {
	dsn := os.Getenv("PERFENG_CLAIM_CHILD_DSN")
	if dsn == "" {
		t.Skip("only launched by restart parent")
	}
	r := openTest(t, dsn)
	oldLease := run.Lease{
		RunID:     os.Getenv("PERFENG_CLAIM_CHILD_ID"),
		Principal: "restart",
		WorkerID:  "restart-worker",
		Token:     os.Getenv("PERFENG_CLAIM_CHILD_TOKEN"),
	}
	claims := claimBatch(t, r, "restart-worker", 10)
	if os.Getenv("PERFENG_CLAIM_CHILD_MODE") == "active" {
		if len(claims) != 0 {
			t.Fatal("process restart stole a live lease")
		}
		stored, found, err := r.GetExecution(testContext, oldLease)
		if err != nil || !found || stored != executionFor(oldLease.RunID) {
			t.Fatal("execution identity was not recovered after restart", err)
		}

		return
	}
	if _, _, err := r.GetExecution(testContext, oldLease); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatal("expired process retained execution access", err)
	}
	if len(claims) != 1 || claims[0].Run.ID != os.Getenv("PERFENG_CLAIM_CHILD_ID") ||
		claims[0].Lease.Token == os.Getenv("PERFENG_CLAIM_CHILD_TOKEN") {
		t.Fatal("expired work not safely recovered")
	}
	stored, found, err := r.GetExecution(testContext, claims[0].Lease)
	if err != nil || !found || stored != executionFor(claims[0].Run.ID) {
		t.Fatal("new owner did not recover execution identity", err)
	}
}

func TestReconciliationMigrationUpgrade(t *testing.T) {
	r := openTest(t, testDatabase(t))
	// Recreate the already-shipped v1 database, including a run and evidence.
	if _, err := r.db.Exec(`CREATE SCHEMA perfeng_control;
		CREATE TABLE perfeng_control.schema_migrations(
			version text PRIMARY KEY,sha256 text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	first, err := migrations.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(string(first)); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(first)
	if _, err := r.db.Exec("INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES ($1,$2)", "0001_initial.sql", hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	a := accepted(t, r, "upgrade", "request-key-upgrade")
	artifact := artifactFor(a.Run.ID)
	if err := r.RegisterArtifact(testContext, "upgrade", artifact); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	claim := oneClaim(t, r, "upgrade-worker")
	if !reflect.DeepEqual(claim.Run, a.Run) {
		t.Fatal("upgrade changed run snapshot")
	}
	if execution, found, err := r.GetExecution(testContext, claim.Lease); err != nil ||
		found || execution != (kubernetes.Execution{}) {
		t.Fatal("upgrade fabricated an execution identity", execution, found, err)
	}
	if replay := accepted(t, r, "upgrade", "request-key-upgrade"); replay != a {
		t.Fatal("upgrade changed acceptance binding")
	}
	var baselines int
	if err := r.db.QueryRow("SELECT count(*) FROM perfeng_control.baselines").Scan(&baselines); err != nil || baselines != 0 {
		t.Fatal("upgrade fabricated a baseline", err)
	}
	got, err := r.GetArtifact(testContext, "upgrade", a.Run.ID, artifact.ID)
	if err != nil || got != artifact {
		t.Fatal("upgrade lost artifact", err)
	}
}
