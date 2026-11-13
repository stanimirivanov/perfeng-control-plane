package app

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type normalizationExecutionTrust struct {
	ExecutionTrust
	plans reconcile.AnalysisPlanResolver
}

func (trust normalizationExecutionTrust) ResolveAnalysisPlan(ctx context.Context, principal string, current run.Run, input reconcile.AnalysisInput) (reconcile.AnalysisPlan, error) {
	return trust.plans.ResolveAnalysisPlan(ctx, principal, current, input)
}

type normalizationArtifactTrust struct {
	ArtifactTrust
	approval reconcile.NormalizedManifestApprover
}

func (trust normalizationArtifactTrust) ApproveNormalizedManifest(ctx context.Context, principal string, current run.Run, input reconcile.AnalysisInput, manifest normalizedresult.Manifest) error {
	return trust.approval.ApproveNormalizedManifest(ctx, principal, current, input, manifest)
}

func composeRerunNormalization(enabled bool, database Database, execution ExecutionTrust, artifacts ArtifactTrust) (ExecutionTrust, ArtifactTrust, error) {
	if !enabled {
		return execution, artifacts, nil
	}
	store, ok := database.(reconcile.RerunReportStore)
	if !ok {
		return nil, nil, ErrInvalidConfiguration
	}
	review, ok := execution.(reconcile.RerunNormalizationReview)
	if !ok || artifacts == nil {
		return nil, nil, ErrInvalidConfiguration
	}
	guarded, err := reconcile.NewRerunNormalizationResolver(store, review)
	if err != nil {
		return nil, nil, ErrInvalidConfiguration
	}
	return normalizationExecutionTrust{ExecutionTrust: execution, plans: guarded}, normalizationArtifactTrust{ArtifactTrust: artifacts, approval: guarded}, nil
}
