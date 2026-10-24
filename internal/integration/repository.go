// Package integration defines the durable correlated-request admission boundary.
package integration

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrUnsupportedRevision rejects a source algorithm the native API cannot represent.
var ErrUnsupportedRevision = errors.New("unsupported source revision")

// Review resolves immutable registry authority and execution target. It must be
// concurrent-safe and perform no I/O except through the supplied baseline reader.
// That reader holds approval locks through admission commit. Forbidden/validation
// errors and ErrUnsupportedRevision are durable rejection; other errors abort
// without reserving identities.
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
	// CancelIntegration binds a cancellation delivery to the full retained context.
	// It returns the current mapped Run, including concurrent terminal outcomes;
	// unknown/invisible requests are NotFound, rejected requests/reuse conflict.
	CancelIntegration(ctx context.Context, principal string, message contract.IntegrationMessage) (Record, error)
}

// RecoveryStore cancels overdue accepted work in bounded database-clock batches.
// Calls are concurrent-safe and restartable; cancellation does not confirm that
// external work stopped. Retained dispositions and identities are never removed.
type RecoveryStore interface {
	// RecoverIntegration processes at most limit overdue Runs. Limits outside
	// 1..100 return run.ErrValidation; failed batches are safe to retry.
	RecoverIntegration(context.Context, int) error
}
