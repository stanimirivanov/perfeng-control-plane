package reconcile

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

// RerunNormalizationReview selects and approves current source-owner
// normalization while preserving the accepted workload and evidence schema.
type RerunNormalizationReview interface {
	AnalysisPlanResolver
	NormalizedManifestApprover
	ResolveRerunAnalysisPlan(context.Context, runrerun.Acceptance, run.Run, AnalysisInput) (AnalysisPlan, error)
	ApproveRerunNormalizedManifest(context.Context, runrerun.Acceptance, run.Run, AnalysisInput, normalizedresult.Manifest) error
}

// RerunNormalizationResolver admits only exact registered raw inputs for known
// children. It establishes neither a historical runtime snapshot nor lease ownership.
type RerunNormalizationResolver struct {
	store  RerunReportStore
	review RerunNormalizationReview
}

// NewRerunNormalizationResolver requires immutable association/artifact readers
// and independent review; construction performs no I/O.
func NewRerunNormalizationResolver(store RerunReportStore, review RerunNormalizationReview) (*RerunNormalizationResolver, error) {
	if store == nil || review == nil {
		return nil, run.ErrValidation
	}
	return &RerunNormalizationResolver{store: store, review: review}, nil
}

func (resolver *RerunNormalizationResolver) lookup(ctx context.Context, principal string, current run.Run, input AnalysisInput) (runrerun.Acceptance, bool, error) {
	if err := ctx.Err(); err != nil {
		return runrerun.Acceptance{}, false, err
	}
	value, known, err := resolver.store.LookupRerunExecution(ctx, principal, current.Clone())
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		return runrerun.Acceptance{}, known, err
	}
	if !known {
		return value, false, nil
	}
	if value.Validate() != nil || value.ChildPrincipal != principal || value.Child.ID != current.ID || value.Child.Request != current.Request {
		return runrerun.Acceptance{}, true, run.ErrUnavailable
	}
	if current.State != run.StateAnalyzing || (RawArtifactSet{Manifest: input.Manifest, Artifacts: input.Sources}).Validate(current.ID) != nil {
		return runrerun.Acceptance{}, true, run.ErrValidation
	}
	for _, reference := range append([]run.Artifact{input.Manifest}, input.Sources...) {
		registered, err := resolver.store.GetArtifact(ctx, principal, current.ID, reference.ID)
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		if err != nil {
			return runrerun.Acceptance{}, true, err
		}
		if registered != reference {
			return runrerun.Acceptance{}, true, run.ErrConflict
		}
	}
	return value, true, nil
}

// ResolveAnalysisPlan uses current source review only after exact registered
// input checks. Known failures never permit ordinary producer/profile fallback.
func (resolver *RerunNormalizationResolver) ResolveAnalysisPlan(ctx context.Context, principal string, current run.Run, input AnalysisInput) (AnalysisPlan, error) {
	value, known, err := resolver.lookup(ctx, principal, current, input)
	if err != nil {
		return AnalysisPlan{}, err
	}
	if !known {
		return resolver.review.ResolveAnalysisPlan(ctx, principal, current.Clone(), input.Clone())
	}
	return resolver.review.ResolveRerunAnalysisPlan(ctx, value, current.Clone(), input.Clone())
}

// ApproveNormalizedManifest applies the same association/input boundary to output
// attestation; collector byte/window/source checks remain independently mandatory.
func (resolver *RerunNormalizationResolver) ApproveNormalizedManifest(ctx context.Context, principal string, current run.Run, input AnalysisInput, manifest normalizedresult.Manifest) error {
	value, known, err := resolver.lookup(ctx, principal, current, input)
	if err != nil {
		return err
	}
	if !known {
		return resolver.review.ApproveNormalizedManifest(ctx, principal, current.Clone(), input.Clone(), manifest)
	}
	return resolver.review.ApproveRerunNormalizedManifest(ctx, value, current.Clone(), input.Clone(), manifest)
}
