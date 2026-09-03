package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

var (
	// ErrAnalysisPending means idempotent normalization has not produced a
	// durable result yet. The worker may retry without an operational error.
	ErrAnalysisPending = errors.New("normalization result is not ready")
	// ErrAnalysisFailed means the normalization process reached a definitive
	// failure. It is distinct from a performance-regression verdict.
	ErrAnalysisFailed = errors.New("normalization failed")
)

// AnalysisInput identifies the persisted raw-result manifest and its declared
// source evidence. Artifact bytes remain in object storage.
type AnalysisInput struct {
	Manifest run.Artifact
	Sources  []run.Artifact
}

// AnalysisExecutor starts, adopts or observes idempotent normalization for one
// Run. It must use approved configuration and attest the normalized output bytes
// before returning their immutable reference.
type AnalysisExecutor interface {
	Normalize(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error)
}

// AnalysisStore discovers and registers evidence while applying lifecycle
// changes through the current claim.
type AnalysisStore interface {
	ClaimAdvancer
	ListArtifacts(context.Context, string, string) ([]run.Artifact, error)
	RegisterArtifact(context.Context, string, run.Artifact) error
}

// AnalysisConfig bounds one normalization attempt and pending-result polling.
type AnalysisConfig struct {
	AttemptTimeout time.Duration
	RetryAfter     time.Duration
}

// DefaultAnalysisConfig returns conservative normalization attempt timings.
func DefaultAnalysisConfig() AnalysisConfig {
	return AnalysisConfig{AttemptTimeout: 10 * time.Second, RetryAfter: 5 * time.Second}
}

// AnalysisReconciler recovers raw evidence and persists normalized output before
// entering REPORTING. It does not make quality, SLO or regression decisions.
type AnalysisReconciler struct {
	store    AnalysisStore
	executor AnalysisExecutor
	config   AnalysisConfig
}

var _ worker.Reconciler = (*AnalysisReconciler)(nil)

// NewAnalysisReconciler validates dependencies and bounded attempt timings.
func NewAnalysisReconciler(
	store AnalysisStore,
	executor AnalysisExecutor,
	config AnalysisConfig,
) (*AnalysisReconciler, error) {
	if store == nil || executor == nil || config.AttemptTimeout <= 0 ||
		config.AttemptTimeout > 5*time.Minute || config.RetryAfter <= 0 ||
		!run.ValidRetryDelay(config.RetryAfter) {
		return nil, run.ErrValidation
	}

	return &AnalysisReconciler{store: store, executor: executor, config: config}, nil
}

// Reconcile invokes normalization only when no normalized-result reference is
// already durable. Registration may precede an uncertain lifecycle transition;
// the next attempt recovers that output instead of starting duplicate work.
func (reconciler *AnalysisReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}
	if claim.Run.State != run.StateAnalyzing {
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
	input, normalized, err := analysisEvidence(claim.Run.ID, artifacts)
	if err != nil {
		return worker.Result{}, err
	}
	if normalized == nil {
		output, executeErr := reconciler.executor.Normalize(
			ctx,
			claim.Lease.Principal,
			claim.Run.Clone(),
			input.Clone(),
		)
		if executeErr != nil {
			return reconciler.executionError(ctx, claim, executeErr)
		}
		if err = validNormalizedArtifact(claim.Run.ID, output, input); err != nil {
			return worker.Result{}, err
		}
		if err = reconciler.store.RegisterArtifact(ctx, claim.Lease.Principal, output); err != nil {
			return worker.Result{}, err
		}
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{State: run.StateReporting})
}

// Clone isolates an executor from the store's artifact slice.
func (input AnalysisInput) Clone() AnalysisInput {
	input.Sources = append([]run.Artifact(nil), input.Sources...)

	return input
}

func (reconciler *AnalysisReconciler) executionError(
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

	pending := errors.Is(err, ErrAnalysisPending)
	failed := errors.Is(err, ErrAnalysisFailed)
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
			Message: "normalization failed",
		},
	})
}

func analysisEvidence(
	runID string,
	artifacts []run.Artifact,
) (AnalysisInput, *run.Artifact, error) {
	evidence := analysisEvidenceSet{}
	for _, artifact := range artifacts {
		if err := evidence.add(runID, artifact); err != nil {
			return AnalysisInput{}, nil, run.ErrValidation
		}
	}
	if evidence.input.Manifest.ID == "" || len(evidence.input.Sources) == 0 {
		return AnalysisInput{}, nil, run.ErrValidation
	}
	if evidence.normalized != nil &&
		validNormalizedArtifact(runID, *evidence.normalized, evidence.input) != nil {
		return AnalysisInput{}, nil, run.ErrValidation
	}

	return evidence.input, evidence.normalized, nil
}

type analysisEvidenceSet struct {
	input      AnalysisInput
	normalized *run.Artifact
}

func (evidence *analysisEvidenceSet) add(runID string, artifact run.Artifact) error {
	if artifact.RunID != runID || artifact.Validate() != nil {
		return run.ErrValidation
	}
	if artifact.Kind == "raw" {
		return evidence.addRaw(artifact)
	}
	if artifact.Kind == "normalized" {
		return evidence.addNormalized(artifact)
	}

	return run.ErrValidation
}

func (evidence *analysisEvidenceSet) addRaw(artifact run.Artifact) error {
	if artifact.Format != "raw-result/v1" {
		evidence.input.Sources = append(evidence.input.Sources, artifact)

		return nil
	}
	if evidence.input.Manifest.ID != "" || artifact.MediaType != "application/json" {
		return run.ErrValidation
	}
	evidence.input.Manifest = artifact

	return nil
}

func (evidence *analysisEvidenceSet) addNormalized(artifact run.Artifact) error {
	if evidence.normalized != nil || artifact.Format != "normalized-result/v1" ||
		artifact.MediaType != "application/json" {
		return run.ErrValidation
	}
	copy := artifact
	evidence.normalized = &copy

	return nil
}

func validNormalizedArtifact(runID string, output run.Artifact, input AnalysisInput) error {
	if output.RunID != runID || output.Kind != "normalized" ||
		output.MediaType != "application/json" || output.Format != "normalized-result/v1" ||
		output.Validate() != nil || output.ID == input.Manifest.ID || output.URI == input.Manifest.URI {
		return run.ErrValidation
	}
	for _, source := range input.Sources {
		if output.ID == source.ID || output.URI == source.URI {
			return run.ErrValidation
		}
	}

	return nil
}
