package integration

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// PublicationVerifier performs full schema, exact-byte, policy and provenance
// verification against transaction-supplied retained admission. It is trusted
// in-process code: no external I/O or mutable registry/baseline approval lookup.
// Fetch inputs beforehand; return builder/codec-validated canonical bytes for
// exactly this proposal. Storage rejects a different verified payload.
type PublicationVerifier func(context.Context, Record, contract.AnalysisPublication) ([]byte, error)

// PublicationRepository retains complete immutable publications, never drafts.
// Scope arguments come from authentication, not body claims. Calls are concurrent-
// safe; returned payloads are owned wire bytes for the publication consumer codec.
type PublicationRepository interface {
	// RetainAnalysisPublication requires prior closed-codec validation and a
	// trusted verifier for new writes. Exact replay skips verification; different
	// request/event content conflicts. Uncertain commits require identical retry.
	RetainAnalysisPublication(context.Context, string, contract.AnalysisPublication, PublicationVerifier) ([]byte, error)
	// GetAnalysisPublication reads original bytes without current approval reads.
	// Unknown, invisible and not-yet-published requests all return scoped not-found.
	GetAnalysisPublication(context.Context, string, contract.CatalogIdentity) ([]byte, error)
}
