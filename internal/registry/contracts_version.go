package registry

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ResolveContractsVersion selects the exact contracts version authorized for
// evidence verification. It does not reapply candidate admission, so removal of
// a candidate image cannot strand an already accepted Run. Resolution is a
// concurrent read of immutable registry state and performs no external I/O.
func (registry *ReportPolicyRegistry) ResolveContractsVersion(
	ctx context.Context,
	principal string,
	current run.Run,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if registry == nil || principal == "" || current.Request.Validate() != nil ||
		(current.State != run.StateCollecting && current.State != run.StateAnalyzing &&
			current.State != run.StateReporting) {
		return "", run.ErrValidation
	}

	entry, exists := registry.lookup(principal, current.Request)
	if !exists {
		return "", run.ErrForbidden
	}

	return entry.contractsVersion, nil
}
