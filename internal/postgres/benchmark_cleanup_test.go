package postgres

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func cleanupReceipt(t *testing.T, reservation run.BenchmarkReservation) []byte {
	t.Helper()
	data, err := json.Marshal(run.BenchmarkCleanupProof{SchemaVersion: 1, RunID: reservation.RunID, PlanSHA256: reservation.Intent.PlanSHA256,
		ClusterUID: reservation.Intent.ClusterUID, NamespaceUID: reservation.NamespaceUID,
		ExecutorStopped: true, WorkloadStopped: true, AuthorityRemoved: true, NamespaceFenced: true})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func drainCleanup(t *testing.T, repository *Repository, id string) {
	t.Helper()
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.benchmark_target_reservations
		SET cleanup_started_at=clock_timestamp()-interval '5 minutes',
		    cleanup_fenced_at=clock_timestamp()-interval '4 minutes',
		    cleanup_not_before=clock_timestamp()-interval '2 minutes'
		WHERE run_id=$1
	`, id); err != nil {
		t.Fatal(err)
	}
}

func TestBenchmarkCleanupCutoffDrainAndAtomicCancellation(t *testing.T) {
	repository, dsn := claimsDB(t)
	claim := benchmarkClaim(t, repository, "alice", "cleanup-cancel-request")
	intent := benchmarkIntent()
	reservation, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("admission"))
	if err != nil {
		t.Fatal(err)
	}
	claim.Run, err = repository.Cancel(testContext, "alice", claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err = repository.BeginBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateAborted})
	if err != nil {
		t.Fatal(err)
	}
	if !reservation.Cleanup.FencedAt.IsZero() || reservation.Cleanup.Ready {
		t.Fatal("unconfirmed barrier started drain")
	}
	if _, err := repository.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, reservation)); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("unfenced release: %v", err)
	}
	if _, err := repository.db.Exec(`
		UPDATE perfeng_control.benchmark_target_reservations
		SET cleanup_started_at = clock_timestamp() - interval '5 minutes',
		    cleanup_not_before = clock_timestamp() - interval '3 minutes'
		WHERE run_id = $1
	`, claim.Run.ID); err != nil {
		t.Fatal(err)
	}
	reservation, err = repository.RecordBenchmarkCleanupFence(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Cleanup.Ready || reservation.Cleanup.NotBefore.Sub(reservation.Cleanup.FencedAt) != run.BenchmarkCleanupGrace {
		t.Fatal("worker outage before quota confirmation bypassed drain")
	}
	retry, err := repository.RecordBenchmarkCleanupFence(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256)
	if err != nil || !retry.Cleanup.NotBefore.Equal(reservation.Cleanup.NotBefore) {
		t.Fatalf("drain clock moved: %v", err)
	}
	if _, err := repository.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, reservation)); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("early release: %v", err)
	}
	if _, err := repository.BindBenchmarkNamespace(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, "00000000-0000-4000-8000-000000000002"); err == nil {
		t.Fatal("cleanup admitted materialization")
	}
	drainCleanup(t, repository, claim.Run.ID)
	restarted := openTest(t, dsn)
	stored, found, err := restarted.GetBenchmarkReservation(testContext, claim.Lease)
	if err != nil || !found || !stored.Cleanup.Ready {
		t.Fatalf("restart lost drain: %v", err)
	}
	if _, err := restarted.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, []byte(`{"verified":true}`)); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("unbound proof accepted: %v", err)
	}
	next, err := restarted.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, stored))
	if err != nil || next.State != run.StateAborted {
		t.Fatalf("cancel not atomic: %v, %s", err, next.State)
	}
	if _, _, err := restarted.GetBenchmarkReservation(testContext, claim.Lease); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("terminal lease retained: %v", err)
	}
	var receipt []byte
	if err := restarted.db.QueryRow(`SELECT cleanup_receipt FROM perfeng_control.benchmark_target_reservations WHERE run_id=$1 AND released_at IS NOT NULL`, claim.Run.ID).Scan(&receipt); err != nil || len(receipt) == 0 {
		t.Fatalf("lost audit: %v", err)
	}
	other := benchmarkClaim(t, restarted, "bob", "cleanup-reuse-request")
	if _, err := restarted.ReserveBenchmark(testContext, other.Lease, other.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatalf("cleaned target remained locked: %v", err)
	}
}

func TestBenchmarkCleanupRecoveryFencesOldWorkerAndPreservesTerminalRun(t *testing.T) {
	repository, dsn := claimsDB(t)
	claim := benchmarkClaim(t, repository, "alice", "cleanup-terminal")
	intent := benchmarkIntent()
	if _, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatal(err)
	}
	cancelled, err := repository.Cancel(testContext, "alice", claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim.Run = cancelled
	terminal, err := repository.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: run.StateAborted})
	if err != nil {
		t.Fatal(err)
	}
	restarted := openTest(t, dsn)
	fresh := oneClaim(t, restarted, "cleanup-recovery-worker")
	if fresh.Run.State != run.StateAborted || fresh.Lease.Token == claim.Lease.Token {
		t.Fatal("terminal hold not rediscovered with fresh authority")
	}
	if _, err := restarted.BeginBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateAborted}); !errors.Is(err, run.ErrLeaseLost) {
		t.Fatalf("stale worker cleanup: %v", err)
	}
	if _, err := restarted.BeginBenchmarkCleanup(testContext, fresh.Lease, fresh.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateAborted}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.BindBenchmarkCleanupNamespace(testContext, fresh.Lease, fresh.Run.Revision, intent.PlanSHA256, "00000000-0000-4000-8000-000000000002"); err != nil {
		t.Fatal(err)
	}
	reservation, _, err := restarted.GetBenchmarkReservation(testContext, fresh.Lease)
	if err != nil {
		t.Fatal(err)
	}
	drainCleanup(t, restarted, fresh.Run.ID)
	result, err := restarted.FinishBenchmarkCleanup(testContext, fresh.Lease, fresh.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, reservation))
	if err != nil || !reflect.DeepEqual(result, terminal) {
		t.Fatalf("terminal snapshot rewritten: %v", err)
	}
	claims, err := restarted.ClaimRuns(testContext, "cleanup-after-release", 1, 30*time.Second)
	if err != nil || len(claims) != 0 {
		t.Fatalf("released terminal rediscovered: %v", err)
	}
}

func TestBenchmarkCleanupReleasesBeforeAnalysisWithoutRevisionRewrite(t *testing.T) {
	repository, _ := claimsDB(t)
	claim := benchmarkClaim(t, repository, "alice", "cleanup-analysis-request")
	intent := benchmarkIntent()
	if _, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatal(err)
	}
	for _, state := range []run.State{run.StateRunning, run.StateCollecting, run.StateAnalyzing} {
		var err error
		claim.Run, err = repository.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	reservation, err := repository.BeginBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateAnalyzing})
	if err != nil {
		t.Fatal(err)
	}
	drainCleanup(t, repository, claim.Run.ID)
	result, err := repository.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, reservation))
	if err != nil || !reflect.DeepEqual(result, claim.Run) {
		t.Fatalf("cleanup rewrote analysis snapshot: %v", err)
	}
	stored, found, err := repository.GetBenchmarkReservation(testContext, claim.Lease)
	if err != nil || !found || stored.Cleanup.ReleasedAt.IsZero() || len(stored.Cleanup.Receipt) == 0 {
		t.Fatalf("analysis lost release audit or current authority: %v", err)
	}
	if _, err := repository.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, cleanupReceipt(t, stored)); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("released audit was rewritten: %v", err)
	}
}

func TestBenchmarkCleanupCancellationWinsCollectionAndConcurrentRelease(t *testing.T) {
	repository, dsn := claimsDB(t)
	claim := benchmarkClaim(t, repository, "alice", "cleanup-race-request")
	intent := benchmarkIntent()
	if _, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("admission")); err != nil {
		t.Fatal(err)
	}
	var err error
	claim.Run, err = repository.AdvanceClaim(testContext, claim.Lease, claim.Run.Revision, run.Change{State: run.StateRunning})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := repository.BeginBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, run.Change{State: run.StateCollecting})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReserveBenchmark(testContext, claim.Lease, claim.Run.Revision, intent, []byte("admission")); !errors.Is(err, run.ErrTransition) {
		t.Fatalf("dispatch reopened: %v", err)
	}
	drainCleanup(t, repository, claim.Run.ID)
	claim.Run, err = repository.Cancel(testContext, "alice", claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := openTest(t, dsn)
	receipt := cleanupReceipt(t, reservation)
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for _, connection := range []*Repository{repository, other} {
		group.Go(func() {
			<-start
			_, err := connection.FinishBenchmarkCleanup(testContext, claim.Lease, claim.Run.Revision, intent.PlanSHA256, receipt)
			errorsFound <- err
		})
	}
	close(start)
	group.Wait()
	close(errorsFound)
	wins := 0
	for err := range errorsFound {
		if err == nil {
			wins++
		} else if !errors.Is(err, run.ErrLeaseLost) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("release winners=%d", wins)
	}
	current, err := repository.Get(testContext, "alice", claim.Run.ID)
	if err != nil || current.State != run.StateAborted {
		t.Fatalf("collection beat cancellation: %v", err)
	}
}
