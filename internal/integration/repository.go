// Package integration defines the durable correlated-request admission boundary.
package integration

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Review resolves immutable registry authority and execution target. It must be
// concurrent-safe and perform no I/O except through the supplied baseline reader.
// That reader holds approval locks through admission commit. Forbidden/validation
// errors are durable rejection; other errors abort without reserving identities.
type Review func(context.Context, string, contract.IntegrationContext,
	func(context.Context, string, string, string) (baseline.Record, error),
) (run.Request, run.ExecutionTarget, error)

// Record owns its disposition and optional current Run snapshot. The original
// disposition is immutable even when Run advances; rejection has no Run.
type Record struct {
	Disposition contract.IntegrationMessage
	Run         *run.Run
}

// Repository retains identities indefinitely until an explicit retirement
// operation is implemented. Inputs require prior wire validation, a full principal
// claim checked against the configured authenticated namespace, and a trusted
// provider issuer. No method authorizes HTTP access.
type Repository interface {
	// AdmitIntegration atomically records the disposition and any new Run.
	// Replay precedes review/deadline checks; context or event reuse conflicts.
	// A commit failure may be uncertain: retry identical identities or read them.
	AdmitIntegration(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review Review) (Record, error)
	// GetIntegration returns current state scoped by principal plus issuer/ID.
	// Unknown and cross-principal requests both return run.ErrNotFound.
	GetIntegration(ctx context.Context, principal string, identity contract.CatalogIdentity) (Record, error)
}
