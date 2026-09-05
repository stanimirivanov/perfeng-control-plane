package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

var (
	// ErrReportPending means idempotent report generation has not published a
	// durable result yet. The worker may retry without an operational error.
	ErrReportPending = errors.New("analysis report is not ready")
	// ErrReportFailed means report generation reached a definitive process or
	// evidence failure. It does not describe a performance verdict.
	ErrReportFailed = errors.New("analysis report generation failed")
)

// ReportingInput identifies the candidate normalized result. Policy and
// approved-reference resolution remain executor responsibilities.
type ReportingInput struct {
	Candidate run.Artifact
}

// ReportExecutor starts, adopts or observes idempotent report generation. It
// must resolve the exact accepted policy, select only an approved reference and
// attest the returned analysis-result bytes before returning their reference.
type ReportExecutor interface {
	// Report returns a verified analysis-result reference, ErrReportPending while
	// work remains active, or ErrReportFailed after a definitive failure.
	Report(context.Context, string, run.Run, ReportingInput) (run.Artifact, error)
}

// ReportingStore discovers and registers evidence while applying lifecycle
// changes through the current claim.
type ReportingStore interface {
	ClaimAdvancer
	// ListArtifacts returns the principal-owned Run's durable evidence in stable order.
	ListArtifacts(context.Context, string, string) ([]run.Artifact, error)
	// RegisterArtifact preserves one verified report reference idempotently.
	RegisterArtifact(context.Context, string, run.Artifact) error
}

// ReportingConfig bounds one report attempt and pending-result polling.
type ReportingConfig struct {
	AttemptTimeout time.Duration
	RetryAfter     time.Duration
}

// DefaultReportingConfig returns conservative report attempt timings.
func DefaultReportingConfig() ReportingConfig {
	return ReportingConfig{AttemptTimeout: 10 * time.Second, RetryAfter: 5 * time.Second}
}

// ReportingReconciler recovers normalized evidence and persists a validated
// analysis report before completing the Run.
type ReportingReconciler struct {
	store    ReportingStore
	executor ReportExecutor
	config   ReportingConfig
}

var _ worker.Reconciler = (*ReportingReconciler)(nil)

// NewReportingReconciler validates dependencies and bounded attempt timings.
func NewReportingReconciler(
	store ReportingStore,
	executor ReportExecutor,
	config ReportingConfig,
) (*ReportingReconciler, error) {
	if store == nil || executor == nil || config.AttemptTimeout <= 0 ||
		config.AttemptTimeout > 5*time.Minute || config.RetryAfter <= 0 ||
		!run.ValidRetryDelay(config.RetryAfter) {
		return nil, run.ErrValidation
	}

	return &ReportingReconciler{store: store, executor: executor, config: config}, nil
}

// Reconcile generates a report only when no report reference is already
// durable. Registration before advancement makes an uncertain retry recoverable.
func (reconciler *ReportingReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}
	if claim.Run.State != run.StateReporting {
		return worker.Result{}, ErrStateNotHandled
	}

	ctx, cancel := context.WithTimeout(ctx, reconciler.config.AttemptTimeout)
	defer cancel()

	artifacts, err := reconciler.store.ListArtifacts(
		ctx,
		claim.Lease.Principal,
		claim.Run.ID,
	)
	if err != nil {
		return worker.Result{}, err
	}
	input, report, err := reportingEvidence(claim.Run.ID, artifacts)
	if err != nil {
		return worker.Result{}, err
	}
	if report == nil {
		output, executeErr := reconciler.executor.Report(
			ctx,
			claim.Lease.Principal,
			claim.Run.Clone(),
			input,
		)
		if executeErr != nil {
			return reconciler.executionError(ctx, claim, executeErr)
		}
		if err = validReportArtifact(claim.Run.ID, output, artifacts); err != nil {
			return worker.Result{}, err
		}
		if err = reconciler.store.RegisterArtifact(ctx, claim.Lease.Principal, output); err != nil {
			return worker.Result{}, err
		}
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{State: run.StateCompleted})
}

func (reconciler *ReportingReconciler) executionError(
	ctx context.Context,
	claim run.Claim,
	err error,
) (worker.Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return worker.Result{}, ctxErr
	}
	for _, operational := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		run.ErrLeaseLost,
		run.ErrUnavailable,
		run.ErrArtifactConflict,
	} {
		if errors.Is(err, operational) {
			return worker.Result{}, operational
		}
	}

	pending := errors.Is(err, ErrReportPending)
	failed := errors.Is(err, ErrReportFailed)
	if pending && failed {
		return worker.Result{}, run.ErrValidation
	}
	if pending {
		return worker.Result{RetryAfter: reconciler.config.RetryAfter}, nil
	}
	if !failed {
		return worker.Result{}, err
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{
		State: run.StateInfrastructureFailure,
		Failure: &run.Failure{
			Code:    run.FailureCodeAnalysisError,
			Message: "analysis report generation failed",
		},
	})
}

func reportingEvidence(
	runID string,
	artifacts []run.Artifact,
) (ReportingInput, *run.Artifact, error) {
	var input ReportingInput
	var report *run.Artifact
	for _, artifact := range artifacts {
		if artifact.RunID != runID || artifact.Validate() != nil {
			return ReportingInput{}, nil, run.ErrValidation
		}
		if artifact.Kind == "raw" {
			continue
		}
		if artifact.Kind != "normalized" || artifact.MediaType != "application/json" {
			return ReportingInput{}, nil, run.ErrValidation
		}
		switch artifact.Format {
		case "normalized-result/v1":
			if input.Candidate.ID != "" {
				return ReportingInput{}, nil, run.ErrValidation
			}
			input.Candidate = artifact
		case "analysis-result/v1":
			if report != nil {
				return ReportingInput{}, nil, run.ErrValidation
			}
			copy := artifact
			report = &copy
		default:
			return ReportingInput{}, nil, run.ErrValidation
		}
	}
	if input.Candidate.ID == "" {
		return ReportingInput{}, nil, run.ErrValidation
	}
	if report != nil && validReportArtifact(runID, *report, artifactsWithout(*report, artifacts)) != nil {
		return ReportingInput{}, nil, run.ErrValidation
	}

	return input, report, nil
}

func validReportArtifact(runID string, output run.Artifact, existing []run.Artifact) error {
	if output.RunID != runID || output.Kind != "normalized" ||
		output.MediaType != "application/json" || output.Format != "analysis-result/v1" ||
		output.Validate() != nil {
		return run.ErrValidation
	}
	for _, artifact := range existing {
		if output.ID == artifact.ID || output.URI == artifact.URI {
			return run.ErrValidation
		}
	}

	return nil
}

func artifactsWithout(excluded run.Artifact, artifacts []run.Artifact) []run.Artifact {
	result := make([]run.Artifact, 0, len(artifacts)-1)
	removed := false
	for _, artifact := range artifacts {
		if !removed && artifact == excluded {
			removed = true
			continue
		}
		result = append(result, artifact)
	}

	return result
}
