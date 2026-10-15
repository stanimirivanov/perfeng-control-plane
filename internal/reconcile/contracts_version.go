package reconcile

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ContractsVersionResolver selects the exact evidence contracts version from
// trusted Run context, never from an artifact's claims.
type ContractsVersionResolver interface {
	// ResolveContractsVersion returns the authorized entry's version for a
	// COLLECTING, ANALYZING or REPORTING Run. Invalid context returns
	// run.ErrValidation; an unmatched principal or resource context returns
	// run.ErrForbidden. Cancellation retains its identity. Implementations must
	// support concurrent reads without changing the Run or reading evidence.
	ResolveContractsVersion(context.Context, string, run.Run) (string, error)
}

func evidenceContractsVersion(
	ctx context.Context,
	resolver ContractsVersionResolver,
	principal string,
	current run.Run,
) (string, error) {
	version, err := resolver.ResolveContractsVersion(ctx, principal, current.Clone())
	if err != nil {
		return "", err
	}
	if !rawresult.ValidContractsVersion(version) {
		return "", run.ErrValidation
	}

	return version, nil
}
