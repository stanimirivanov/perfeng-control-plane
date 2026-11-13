package registry

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

// ResolveRerunAnalysisPlan requires trusted worker-validated acceptance and exact
// source-owner workload/schema before ordinary reviewed normalization selection.
func (registry *ReportPolicyRegistry) ResolveRerunAnalysisPlan(ctx context.Context, value runrerun.Acceptance, current run.Run, input reconcile.AnalysisInput) (reconcile.AnalysisPlan, error) {
	if err := ctx.Err(); err != nil {
		return reconcile.AnalysisPlan{}, err
	}
	if err := registry.reviewRerunNormalization(value, current); err != nil {
		return reconcile.AnalysisPlan{}, err
	}
	return registry.ResolveAnalysisPlan(ctx, value.Source.OwnerPrincipal, current.Clone(), input.Clone())
}

// ApproveRerunNormalizedManifest preserves the same source-owner context and
// frozen pins as planning; current normalizer and exact source claims must match.
func (registry *ReportPolicyRegistry) ApproveRerunNormalizedManifest(ctx context.Context, value runrerun.Acceptance, current run.Run, input reconcile.AnalysisInput, manifest normalizedresult.Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := registry.reviewRerunNormalization(value, current); err != nil {
		return err
	}
	return registry.ApproveNormalizedManifest(ctx, value.Source.OwnerPrincipal, current.Clone(), input.Clone(), manifest)
}

func (registry *ReportPolicyRegistry) reviewRerunNormalization(value runrerun.Acceptance, current run.Run) error {
	if registry == nil || current.State != run.StateAnalyzing || current.Request.Validate() != nil || value.Source.OwnerPrincipal == "" || value.Child.ID != current.ID || value.Child.Request != current.Request || value.Inputs.Request != current.Request {
		return run.ErrValidation
	}
	entry, exists := registry.lookup(value.Source.OwnerPrincipal, current.Request)
	if !exists || entry.executionTarget != run.ExecutionTargetKubernetes || entry.benchmark != nil || entry.workload != value.Inputs.Workload || entry.contractsVersion != value.Inputs.ContractsVersion {
		return run.ErrForbidden
	}
	return nil
}
