package registry

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

// ApproveRerunRawManifest requires a trusted worker-validated acceptance, exact
// frozen workload/schema and an OCI producer present in its sealed Job. Current
// source-owner review remains required; this is not historical producer metadata
// or caller authentication and performs no registry reselection under the child.
func (registry *ReportPolicyRegistry) ApproveRerunRawManifest(ctx context.Context, value runrerun.Acceptance, current run.Run, sealed *batchv1.Job, manifest rawresult.Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || current.State != run.StateCollecting || current.Request.Validate() != nil || value.Source.OwnerPrincipal == "" || value.Child.ID != current.ID || value.Child.Request != current.Request || value.Inputs.Request != current.Request || manifest.Validate(current.ID, value.Inputs.ContractsVersion) != nil {
		return run.ErrValidation
	}
	target, valid := classifyExecution(manifest.Producer, sealed)
	entry, exists := registry.lookup(value.Source.OwnerPrincipal, current.Request)
	if !valid || target != run.ExecutionTargetKubernetes || !exists || entry.executionTarget != run.ExecutionTargetKubernetes || entry.benchmark != nil || manifest.Workload != value.Inputs.Workload {
		return run.ErrForbidden
	}
	return registry.ApproveRawManifest(ctx, value.Source.OwnerPrincipal, current.Clone(), manifest)
}
