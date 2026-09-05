package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

var (
	// ErrArtifactsNotReady means the execution's immutable raw artifacts are not
	// visible yet. The worker may retry without reporting an operational failure.
	ErrArtifactsNotReady = errors.New("raw execution artifacts are not ready")
	// ErrInvalidArtifacts means the execution completed without a trustworthy raw
	// artifact set. It is a test/tool outcome, not an analysis verdict.
	ErrInvalidArtifacts = errors.New("raw execution artifacts are invalid")
)

// RawArtifactSet contains the immutable raw-result manifest and every object
// reference declared by it. The manifest is registered as evidence as well.
type RawArtifactSet struct {
	Manifest  run.Artifact
	Artifacts []run.Artifact
}

// RawArtifactCollector verifies an execution's raw manifest and object bytes.
// It returns stable references only after checking storage ownership, checksum,
// size, media type, format, run identity and approved producer provenance.
type RawArtifactCollector interface {
	// CollectRawArtifacts returns a complete verified set, ErrArtifactsNotReady
	// during publication lag, or ErrInvalidArtifacts for definitive bad evidence.
	CollectRawArtifacts(context.Context, string, run.Run) (RawArtifactSet, error)
}

// CollectionStore registers immutable references and advances an owned claim.
// Registration must be idempotent for an identical artifact and reject an
// existing artifact ID bound to different evidence.
type CollectionStore interface {
	ClaimAdvancer
	// RegisterArtifact preserves one verified reference idempotently.
	RegisterArtifact(context.Context, string, run.Artifact) error
}

// CollectionConfig bounds artifact verification and controls readiness polling.
type CollectionConfig struct {
	AttemptTimeout time.Duration
	RetryAfter     time.Duration
}

// DefaultCollectionConfig returns a short verification deadline and a quiet
// delay for storage visibility after an execution terminates.
func DefaultCollectionConfig() CollectionConfig {
	return CollectionConfig{AttemptTimeout: 10 * time.Second, RetryAfter: 5 * time.Second}
}

// CollectionReconciler registers verified raw references before entering
// ANALYZING. It never reads arbitrary artifact locations from the public API.
type CollectionReconciler struct {
	store     CollectionStore
	collector RawArtifactCollector
	config    CollectionConfig
}

var _ worker.Reconciler = (*CollectionReconciler)(nil)

// NewCollectionReconciler validates dependencies and bounded attempt timings.
func NewCollectionReconciler(
	store CollectionStore,
	collector RawArtifactCollector,
	config CollectionConfig,
) (*CollectionReconciler, error) {
	if store == nil || collector == nil || config.AttemptTimeout <= 0 ||
		config.AttemptTimeout > 5*time.Minute || config.RetryAfter <= 0 ||
		!run.ValidRetryDelay(config.RetryAfter) {
		return nil, run.ErrValidation
	}

	return &CollectionReconciler{store: store, collector: collector, config: config}, nil
}

// Reconcile verifies and registers one complete raw artifact set. Registration
// may be repeated after partial or uncertain outcomes because references are
// immutable and idempotent; lifecycle advancement happens only after all writes.
func (reconciler *CollectionReconciler) Reconcile(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	if !validOwnedClaim(claim) {
		return worker.Result{}, run.ErrValidation
	}
	if claim.Run.State != run.StateCollecting {
		return worker.Result{}, ErrStateNotHandled
	}

	ctx, cancel := context.WithTimeout(ctx, reconciler.config.AttemptTimeout)
	defer cancel()

	collected, err := reconciler.collector.CollectRawArtifacts(
		ctx,
		claim.Lease.Principal,
		claim.Run.Clone(),
	)
	if err != nil {
		switch {
		case errors.Is(err, ErrArtifactsNotReady) && errors.Is(err, ErrInvalidArtifacts):
			return worker.Result{}, run.ErrValidation
		case errors.Is(err, ErrArtifactsNotReady):
			return worker.Result{RetryAfter: reconciler.config.RetryAfter}, nil
		case errors.Is(err, ErrInvalidArtifacts):
			return reconciler.reject(ctx, claim)
		default:
			return worker.Result{}, err
		}
	}
	if err := collected.Validate(claim.Run.ID); err != nil {
		return worker.Result{}, err
	}

	for _, artifact := range collected.Artifacts {
		if err := reconciler.store.RegisterArtifact(ctx, claim.Lease.Principal, artifact); err != nil {
			return worker.Result{}, err
		}
	}
	if err := reconciler.store.RegisterArtifact(
		ctx,
		claim.Lease.Principal,
		collected.Manifest,
	); err != nil {
		return worker.Result{}, err
	}

	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{State: run.StateAnalyzing})
}

func (reconciler *CollectionReconciler) reject(
	ctx context.Context,
	claim run.Claim,
) (worker.Result, error) {
	return advanceOwnedClaim(ctx, reconciler.store, claim, run.Change{
		State: run.StateTestFailure,
		Failure: &run.Failure{
			Code:    run.FailureCodeToolError,
			Message: "execution did not produce valid raw artifacts",
		},
	})
}

// Validate checks the trusted collector boundary before any reference is stored.
func (collected RawArtifactSet) Validate(runID string) error {
	if len(collected.Artifacts) == 0 || collected.Manifest.RunID != runID ||
		collected.Manifest.Kind != "raw" || collected.Manifest.MediaType != "application/json" ||
		collected.Manifest.Format != "raw-result/v1" || collected.Manifest.Validate() != nil {
		return run.ErrValidation
	}

	ids := map[string]struct{}{collected.Manifest.ID: {}}
	locations := map[string]struct{}{collected.Manifest.URI: {}}
	for _, artifact := range collected.Artifacts {
		if artifact.RunID != runID || artifact.Kind != "raw" || artifact.Validate() != nil {
			return run.ErrValidation
		}
		if _, exists := ids[artifact.ID]; exists {
			return run.ErrValidation
		}
		if _, exists := locations[artifact.URI]; exists {
			return run.ErrValidation
		}
		ids[artifact.ID] = struct{}{}
		locations[artifact.URI] = struct{}{}
	}

	return nil
}
