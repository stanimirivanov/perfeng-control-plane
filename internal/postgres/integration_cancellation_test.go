package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationCancellationRetainsDispositionAndFencesConflicts(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	message := integrationRequest(t, r, "cancel")
	request := testRequest(t)
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return request, run.ExecutionTargetKubernetes, nil
	}
	admitted, err := r.AdmitIntegration(testContext, "alice", "https://provider.example", message, review)
	if err != nil {
		t.Fatal(err)
	}
	cancellation := message
	cancellation.ID += "-cancel"
	cancellation.Type = "dev.perfeng.integration.cancel-requested.v1"
	cancellation.Data.Reason = "user"
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := r.CancelIntegration(testContext, "alice", cancellation)
			if err != nil || got.Run == nil || got.Run.State != run.StateAborted || got.Run.Revision != 2 {
				t.Error("non-idempotent cancellation", err)
			}
		})
	}
	wg.Wait()
	replayed, err := r.AdmitIntegration(testContext, "alice", "https://provider.example", message, review)
	if err != nil || !reflect.DeepEqual(replayed.Disposition, admitted.Disposition) || replayed.Run.State != run.StateAborted {
		t.Fatal("admission replay changed", err)
	}
	cancellation.Data.Reason = "deadline"
	if _, err := r.CancelIntegration(testContext, "alice", cancellation); !errors.Is(err, run.ErrConflict) {
		t.Fatal("event reuse", err)
	}
	cancellation.ID += "-context"
	cancellation.Data.Context.Profile.Revision = "other"
	if _, err := r.CancelIntegration(testContext, "alice", cancellation); !errors.Is(err, run.ErrConflict) {
		t.Fatal("context reuse", err)
	}
	cancellation = message
	cancellation.ID += "-unknown"
	cancellation.Type = "dev.perfeng.integration.cancel-requested.v1"
	cancellation.Data.Reason = "user"
	cancellation.Data.Context.Request.ID = "unknown"
	if _, err := r.CancelIntegration(testContext, "alice", cancellation); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("unknown cancellation created work", err)
	}
	cancellation.Data.Context.Principal.ID = "bob"
	cancellation.Data.Context.Request = message.Data.Context.Request
	if _, err := r.CancelIntegration(testContext, "bob", cancellation); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("scope leak", err)
	}
}

func TestIntegrationCancellationWaitsForInflightAdmission(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testContext, 10*time.Second)
	defer cancel()
	message := integrationRequest(t, r, "inflight")
	entered, release := make(chan struct{}), make(chan struct{})
	request := testRequest(t)
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		close(entered)
		select {
		case <-release:
			return request, run.ExecutionTargetKubernetes, nil
		case <-ctx.Done():
			return run.Request{}, "", ctx.Err()
		}
	}
	admitted := make(chan error, 1)
	go func() {
		_, err := r.AdmitIntegration(ctx, "alice", "https://provider.example", message, review)
		admitted <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("admission never reached review")
	}
	cancellation := message
	cancellation.Type, cancellation.ID, cancellation.Data.Reason = "dev.perfeng.integration.cancel-requested.v1", message.ID+"-cancel", "user"
	cancelled := make(chan error, 1)
	go func() {
		result, err := r.CancelIntegration(ctx, "alice", cancellation)
		if err == nil && (result.Run == nil || result.Run.State != run.StateAborted) {
			err = run.ErrConflict
		}
		cancelled <- err
	}()
	close(release)
	for _, done := range []chan error{admitted, cancelled} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("admission/cancellation did not settle")
		}
	}
}

func TestIntegrationDeadlineRecoveryIsBoundedAndRestartSafe(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	request := testRequest(t)
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return request, run.ExecutionTargetKubernetes, nil
	}
	type fixture struct {
		message contract.IntegrationMessage
		current run.Run
	}
	var fixtures []fixture
	for _, name := range []string{"created", "running", "terminal", "future"} {
		message := integrationRequest(t, r, name)
		got, err := r.AdmitIntegration(testContext, "alice", "https://provider.example", message, review)
		if err != nil {
			t.Fatal(err)
		}
		current := *got.Run
		if name == "running" || name == "terminal" {
			for _, state := range []run.State{run.StateValidating, run.StateProvisioning, run.StateRunning} {
				current, err = r.Advance(testContext, "alice", current.ID, current.Revision, run.Change{State: state})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if name == "terminal" {
			for _, state := range []run.State{run.StateCollecting, run.StateAnalyzing, run.StateReporting, run.StateCompleted} {
				current, err = r.Advance(testContext, "alice", current.ID, current.Revision, run.Change{State: state})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		// Backdate the accepted fixture's context to model elapsed DB time without
		// sleeping. Production never rewrites immutable context or dispositions.
		if name != "future" {
			var now time.Time
			if err := r.db.QueryRow("SELECT clock_timestamp()").Scan(&now); err != nil {
				t.Fatal(err)
			}
			message.Data.Context.Deadline = now.Add(-time.Second).UTC().Format(time.RFC3339Nano)
			got.Disposition.Data.Context = message.Data.Context
			requested, err := time.Parse(time.RFC3339Nano, message.Data.Context.RequestedAt)
			if err != nil {
				t.Fatal(err)
			}
			got.Disposition.Time = requested.Add(time.Second).Format(time.RFC3339Nano)
			claims, _ := json.Marshal(message.Data.Context)
			disposition, _ := json.Marshal(got.Disposition)
			if _, err := r.db.Exec(`UPDATE perfeng_control.integration_requests SET context=$1,disposition=$2 WHERE run_id=$3`, claims, disposition, current.ID); err != nil {
				t.Fatal(err)
			}
		}
		fixtures = append(fixtures, fixture{message, current})
	}
	// A locked expired Run must not block other instances or the bounded batch.
	locked, err := r.begin(testContext)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback()
	if _, err := lockedRun(testContext, locked, "alice", fixtures[0].current.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.RecoverIntegration(testContext, 1); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(testContext, "alice", fixtures[1].current.ID)
	if err != nil || got.State != run.StateCancelling {
		t.Fatal("active deadline falsely stopped or skipped", err)
	}
	if err := locked.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = openTest(t, dsn)
	if err := r.RecoverIntegration(testContext, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.RecoverIntegration(testContext, 100); err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		got, err := r.GetIntegration(testContext, "alice", fixture.message.Data.Context.Request)
		if err != nil {
			t.Fatal(err)
		}
		want := []run.State{run.StateAborted, run.StateCancelling, run.StateCompleted, run.StateCreated}[i]
		if got.Run.State != want {
			t.Fatal("incorrect recovered state", i, got.Run.State)
		}
		if i >= 2 && got.Run.Revision != fixture.current.Revision {
			t.Fatal("terminal/future Run changed")
		}
		if i == 0 {
			fixture.message.ID += "-late"
			replay, err := r.AdmitIntegration(testContext, "alice", "https://provider.example", fixture.message, review)
			if err != nil || replay.Run.ID != fixture.current.ID || replay.Disposition.Type != "dev.perfeng.integration.accepted.v1" {
				t.Fatal("late replay redispatched", err)
			}
		}
	}
	// Cancellation after completion returns the frozen terminal revision.
	cancellation := fixtures[2].message
	cancellation.ID += "-cancel"
	cancellation.Type = "dev.perfeng.integration.cancel-requested.v1"
	cancellation.Data.Reason = "user"
	gotTerminal, err := r.CancelIntegration(testContext, "alice", cancellation)
	if err != nil || gotTerminal.Run.Revision != fixtures[2].current.Revision || gotTerminal.Run.State != run.StateCompleted {
		t.Fatal("terminal result overwritten", err)
	}
}
