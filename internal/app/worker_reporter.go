package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

const (
	workerLogMessage = "reconciliation operation failed"
	unknownValue     = "unknown"
)

// WorkerEventReporter emits classified operational failures without logging
// arbitrary error text, provider responses or lease capabilities.
type WorkerEventReporter struct {
	logger *slog.Logger
}

// NewWorkerEventReporter binds worker events to a concurrency-safe structured logger.
func NewWorkerEventReporter(logger *slog.Logger) (*WorkerEventReporter, error) {
	if logger == nil {
		return nil, ErrInvalidConfiguration
	}

	return &WorkerEventReporter{logger: logger}, nil
}

// Report writes one fixed-message error event. A Run ID is included only when
// it satisfies the public Run contract; unknown operation and error values are
// reduced to fixed classifications.
func (reporter *WorkerEventReporter) Report(event worker.Event) {
	if reporter == nil || reporter.logger == nil {
		return
	}
	attributes := []slog.Attr{
		slog.String("component", "reconciliation_worker"),
		slog.String("operation", workerOperation(event.Operation)),
		slog.String("error_class", workerErrorClass(event.Err)),
	}
	if contract.ValidID(event.RunID) {
		attributes = append(attributes, slog.String("run_id", event.RunID))
	}
	reporter.logger.LogAttrs(
		context.Background(),
		slog.LevelError,
		workerLogMessage,
		attributes...,
	)
}

func workerOperation(operation worker.Operation) string {
	switch operation {
	case worker.OperationClaim, worker.OperationRenew,
		worker.OperationReconcile, worker.OperationRelease:
		return string(operation)
	default:
		return unknownValue
	}
}

func workerErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, worker.ErrCancellationObserved):
		return "cancellation_observed"
	case errors.Is(err, run.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, run.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, kubernetes.ErrJobConflict):
		return "job_conflict"
	case errors.Is(err, kubernetes.ErrExecutionConflict):
		return "execution_conflict"
	case errors.Is(err, run.ErrArtifactConflict):
		return "artifact_conflict"
	case errors.Is(err, run.ErrConflict):
		return "conflict"
	case errors.Is(err, run.ErrNotFound):
		return "not_found"
	case errors.Is(err, run.ErrForbidden):
		return "forbidden"
	case errors.Is(err, run.ErrTerminal):
		return "terminal"
	case errors.Is(err, run.ErrTransition):
		return "run_transition"
	case errors.Is(err, baseline.ErrTransition):
		return "baseline_transition"
	case errors.Is(err, run.ErrRevision):
		return "stale_revision"
	case errors.Is(err, reconcile.ErrArtifactsNotReady):
		return "artifacts_not_ready"
	case errors.Is(err, reconcile.ErrInvalidArtifacts):
		return "invalid_artifacts"
	case errors.Is(err, reconcile.ErrAnalysisPending):
		return "analysis_pending"
	case errors.Is(err, reconcile.ErrAnalysisFailed):
		return "analysis_failed"
	case errors.Is(err, reconcile.ErrReportPending):
		return "report_pending"
	case errors.Is(err, reconcile.ErrReportFailed):
		return "report_failed"
	case errors.Is(err, reconcile.ErrExecutionNotBound):
		return "execution_not_bound"
	case errors.Is(err, reconcile.ErrStateNotHandled):
		return "state_not_handled"
	case errors.Is(err, objectstore.ErrObjectNotFound):
		return "object_not_found"
	case errors.Is(err, objectstore.ErrObjectMismatch):
		return "object_mismatch"
	case errors.Is(err, run.ErrValidation):
		return "validation"
	default:
		return unknownValue
	}
}
