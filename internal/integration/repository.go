// Package integration defines the durable correlated-request admission boundary.
package integration

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrUnsupportedRevision rejects a source algorithm the native API cannot represent.
var ErrUnsupportedRevision = errors.New("unsupported source revision")

// AdmissionResolution owns the native values from one successful registry review.
// Baseline is nil only for an approved SLO-only selection; otherwise it is the
// exact approved snapshot verified against the requested immutable pin. Workload
// is resolved explicitly, never inferred from catalog labels or Request.TestSuite.
// This value is not durable admission evidence and grants no publication authority.
type AdmissionResolution struct {
	Request  run.Request
	Target   run.ExecutionTarget
	Workload rawresult.Identity
	Baseline *baseline.Record
	// Verification is an owned snapshot from the same reviewed entry. Nil means
	// absent legacy/custom review evidence, never permission to infer provenance.
	// Native capture retains non-nil evidence atomically for new admissions.
	Verification *AnalysisVerification
}

// Review resolves immutable registry authority and execution target. It must be
// concurrent-safe and perform no I/O except through the supplied baseline reader.
// That reader holds approval locks through admission commit. Forbidden/validation
// errors and ErrUnsupportedRevision are durable rejection; other errors abort
// without reserving identities.
type Review func(context.Context, string, contract.IntegrationContext,
	func(context.Context, string, string, string) (baseline.Record, error),
) (run.Request, run.ExecutionTarget, error)

// ResolvedReview returns owned native authority for atomic capture. It has Review's
// no-external-I/O contract and must use exactly one supplied locked baseline read
// for a non-nil Baseline, or none for SLO-only selection. Returned baseline content
// must equal that locked snapshot. It performs no historical reconstruction.
type ResolvedReview func(context.Context, string, contract.IntegrationContext,
	func(context.Context, string, string, string) (baseline.Record, error),
) (AdmissionResolution, error)

// Record owns its disposition and optional current Run snapshot. The original
// disposition is immutable even when Run advances; rejection has no Run.
type Record struct {
	Disposition contract.IntegrationMessage
	Run         *run.Run
	// Native is nil for legacy/unbound or rejected admission. Non-nil evidence
	// has an explicit nullable Baseline and survives replay without re-review.
	Native *contract.AnalysisNativeBinding
	// Verification is immutable admission-time evidence; nil preserves historical
	// absence. Replay never reviews or backfills it from current registry state.
	Verification *AnalysisVerification
}

// NativeRepository adds atomic binding capture without changing legacy writers.
// Apply the expanded schema before use. Inputs require the same authenticated
// scope/wire validation as Repository; uncertain commits require identical retry.
type NativeRepository interface {
	// AdmitIntegrationResolved captures only new accepted work; replay returns
	// original evidence (possibly absent) without review or historical backfill.
	AdmitIntegrationResolved(context.Context, string, string, contract.IntegrationMessage, ResolvedReview) (Record, error)
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
