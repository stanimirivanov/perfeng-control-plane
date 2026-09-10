package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

func TestWorkerEventReporterEmitsSafeStructuredEvent(t *testing.T) {
	var output bytes.Buffer
	reporter, err := NewWorkerEventReporter(slog.New(slog.NewJSONHandler(&output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	secret := "postgres://user:secret@example.invalid/database"
	reporter.Report(worker.Event{
		RunID: "perf-20260910-120000-12345678", Operation: worker.OperationReconcile,
		Err: errors.Join(run.ErrUnavailable, errors.New(secret)),
	})

	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "postgres") {
		t.Fatalf("log disclosed error detail: %s", output.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"level":       "ERROR",
		"msg":         workerLogMessage,
		"component":   "reconciliation_worker",
		"operation":   "reconcile",
		"error_class": "unavailable",
		"run_id":      "perf-20260910-120000-12345678",
	} {
		if entry[key] != want {
			t.Errorf("%s = %#v, want %q", key, entry[key], want)
		}
	}
}

func TestWorkerEventReporterRejectsUntrustedDimensions(t *testing.T) {
	var output bytes.Buffer
	reporter, err := NewWorkerEventReporter(slog.New(slog.NewJSONHandler(&output, nil)))
	if err != nil {
		t.Fatal(err)
	}
	runID := "invalid-run-id token=secret"
	operation := worker.Operation("invalid-operation token=secret")
	reporter.Report(worker.Event{RunID: runID, Operation: operation, Err: errors.New("token=secret")})

	if strings.Contains(output.String(), "token=secret") || strings.Contains(output.String(), runID) ||
		strings.Contains(output.String(), string(operation)) {
		t.Fatalf("log disclosed untrusted dimensions: %s", output.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["operation"] != unknownValue || entry["error_class"] != unknownValue {
		t.Fatalf("entry = %#v", entry)
	}
	if _, exists := entry["run_id"]; exists {
		t.Fatalf("invalid run_id was logged: %#v", entry)
	}
}

func TestWorkerErrorClassRecognizesOperationalErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{worker.ErrCancellationObserved, "cancellation_observed"},
		{run.ErrLeaseLost, "lease_lost"},
		{run.ErrUnavailable, "unavailable"},
		{kubernetes.ErrJobConflict, "job_conflict"},
		{kubernetes.ErrExecutionConflict, "execution_conflict"},
		{run.ErrArtifactConflict, "artifact_conflict"},
		{run.ErrConflict, "conflict"},
		{run.ErrNotFound, "not_found"},
		{run.ErrForbidden, "forbidden"},
		{run.ErrTerminal, "terminal"},
		{run.ErrTransition, "run_transition"},
		{baseline.ErrTransition, "baseline_transition"},
		{run.ErrRevision, "stale_revision"},
		{reconcile.ErrArtifactsNotReady, "artifacts_not_ready"},
		{reconcile.ErrInvalidArtifacts, "invalid_artifacts"},
		{reconcile.ErrAnalysisPending, "analysis_pending"},
		{reconcile.ErrAnalysisFailed, "analysis_failed"},
		{reconcile.ErrReportPending, "report_pending"},
		{reconcile.ErrReportFailed, "report_failed"},
		{reconcile.ErrExecutionNotBound, "execution_not_bound"},
		{reconcile.ErrStateNotHandled, "state_not_handled"},
		{objectstore.ErrObjectNotFound, "object_not_found"},
		{objectstore.ErrObjectMismatch, "object_mismatch"},
		{run.ErrValidation, "validation"},
		{errors.New("provider detail"), unknownValue},
		{nil, unknownValue},
	} {
		if actual := workerErrorClass(errors.Join(test.err, errors.New("secret detail"))); actual != test.want {
			t.Errorf("workerErrorClass(%v) = %q, want %q", test.err, actual, test.want)
		}
	}
}

func TestNewWorkerEventReporterRequiresLogger(t *testing.T) {
	if reporter, err := NewWorkerEventReporter(nil); reporter != nil || !errors.Is(
		err, ErrInvalidConfiguration,
	) {
		t.Fatalf("reporter = %#v, error = %v", reporter, err)
	}
	var reporter *WorkerEventReporter
	reporter.Report(worker.Event{})
}
