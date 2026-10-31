package registry

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ResolveNativeBaselinePlan returns a reviewed, complete v2 browser plan for
// one accepted CREATED Run. It does not qualify a worker or issue a lease.
func (registry *ReportPolicyRegistry) ResolveNativeBaselinePlan(
	ctx context.Context,
	principal string,
	current run.Run,
) (nativeworker.BaselinePlan, error) {
	if err := ctx.Err(); err != nil {
		return nativeworker.BaselinePlan{}, err
	}
	if registry == nil || principal == "" || current.State != run.StateCreated ||
		current.Request.Validate() != nil {
		return nativeworker.BaselinePlan{}, run.ErrValidation
	}
	entry, exists := registry.lookup(principal, current.Request)
	if !exists || entry.executionTarget != run.ExecutionTargetNativeLinux ||
		entry.nativeBaseline == nil || entry.rawProducer.Artifact == nil {
		return nativeworker.BaselinePlan{}, run.ErrForbidden
	}
	if _, approved := entry.candidates[current.Request.Candidate.Image]; !approved {
		return nativeworker.BaselinePlan{}, run.ErrForbidden
	}
	plan, err := entry.nativeBaseline.Resolve(
		current.Request.TestSuite,
		entry.workload,
		current.Request.Profile,
		current.Request.Environment.ID,
		*entry.rawProducer.Artifact,
	)
	if err != nil {
		return nativeworker.BaselinePlan{}, run.ErrForbidden
	}

	return plan, nil
}
