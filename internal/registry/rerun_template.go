package registry

import (
	"bytes"
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

// ResolveRerunTemplate reviews a previously validated child-bound acceptance
// against exact current source-owner registry pins. It grants no dispatch permit
// and must not be used to authenticate an acceptance supplied by a browser.
func (registry *ReportPolicyRegistry) ResolveRerunTemplate(ctx context.Context, value runrerun.Acceptance, current run.Run) (*batchv1.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry == nil || (current.State != run.StateValidating && current.State != run.StateProvisioning) || current.Request.Validate() != nil || value.Child.ID != current.ID || value.Child.Request != current.Request || value.Inputs.Request != current.Request || value.Source.OwnerPrincipal == "" {
		return nil, run.ErrValidation
	}
	entry, exists := registry.lookup(value.Source.OwnerPrincipal, current.Request)
	if !exists || entry.executionTarget != run.ExecutionTargetKubernetes || entry.benchmark != nil || entry.workload != value.Inputs.Workload || entry.contractsVersion != value.Inputs.ContractsVersion || !bytes.Equal(entry.policyBytes, value.Inputs.PolicyBytes) || entry.mode != value.Inputs.PolicyMode || !entry.reportProducer.Equal(value.Inputs.Producer) {
		return nil, run.ErrForbidden
	}
	return registry.ResolveJob(ctx, value.Source.OwnerPrincipal, current.Clone())
}
