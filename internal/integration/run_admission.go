package integration

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// RunAdmissionScope identifies a native Run within independently authenticated
// principal and configured provider namespaces. RunID is the native identifier,
// not a request identity or a caller-supplied publication claim.
type RunAdmissionScope struct {
	Provider  string
	Principal contract.CatalogIdentity
	RunID     string
}

// RunAdmissionReader locates retained admission evidence without registry review,
// reconstruction or writes. It grants no execution or publication authority.
// Implementations are concurrent-safe and return owned records, including the
// current Run and original disposition. Missing historical evidence stays nil.
type RunAdmissionReader interface {
	// GetIntegrationForRun returns run.ErrNotFound for unknown, unassociated or
	// invisible Runs. Invalid required scope returns run.ErrValidation; failures
	// return a zero record. Callers must establish the scope independently.
	GetIntegrationForRun(context.Context, RunAdmissionScope) (Record, error)
}
