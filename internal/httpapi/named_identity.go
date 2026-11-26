package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// ErrNamedCredentialRejected is the inbound adapter's safe credential denial.
// Composition translates the verifier's rejection; unknown failures are unavailable.
var ErrNamedCredentialRejected = errors.New("named credential rejected")

// ErrReportScopeNotFound hides scoped delegation denial for an otherwise
// verified report credential. Other named operations retain their admission policy.
var ErrReportScopeNotFound = errors.New("report identity outside scope")

// NamedIdentity is verified actor/caller input to a service operation, never a
// request DTO, legacy principal or permission grant. The operation must check
// current service rights and fence its own mutation authorization separately.
type NamedIdentity struct {
	Actor, Caller serviceaccess.Identity
	ActorKind     string
}

// NamedAuthenticate verifies a bounded opaque credential and current identity
// policy. It returns ErrNamedCredentialRejected for rejection, run.ErrUnavailable
// for unavailable trust, or a context error. Implementations must be concurrent-safe.
type NamedAuthenticate func(context.Context, string) (NamedIdentity, error)

// NamedOperation receives only credential-verified authority. Request headers,
// body and path remain untrusted; this callback owns service/operation authorization.
type NamedOperation func(http.ResponseWriter, *http.Request, NamedIdentity)

type namedIdentityBoundary struct {
	enabled      bool
	authenticate NamedAuthenticate
	operation    NamedOperation
}

// NewNamedIdentityBoundary constructs an explicitly gated authentication seam,
// not a route registration or legacy fallback. Disabled mode returns safe 404
// without invoking dependencies. Construction performs no I/O.
func NewNamedIdentityBoundary(enabled bool, authenticate NamedAuthenticate, operation NamedOperation) (http.Handler, error) {
	if enabled && (authenticate == nil || operation == nil) {
		return nil, errors.New("named authentication and operation required")
	}
	return &namedIdentityBoundary{enabled: enabled, authenticate: authenticate, operation: operation}, nil
}

func (h *namedIdentityBoundary) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		fail(w, 404, "NOT_FOUND")
		return
	}
	if r.Context().Err() != nil {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	token, ok := bearerToken(r)
	if !ok || len(token) > 16<<10 || strings.ContainsFunc(token, func(value rune) bool { return value <= 32 || value >= 127 }) {
		fail(w, 401, "UNAUTHENTICATED")
		return
	}
	identity, err := h.authenticate(r.Context(), token)
	if r.Context().Err() != nil || errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	if errors.Is(err, ErrReportScopeNotFound) {
		fail(w, 404, "NOT_FOUND")
		return
	}
	if errors.Is(err, ErrNamedCredentialRejected) {
		fail(w, 401, "UNAUTHENTICATED")
		return
	}
	if err != nil || !identity.Actor.Valid() || !identity.Caller.Valid() || identity.Actor.Issuer != identity.Caller.Issuer || (identity.ActorKind != "HUMAN" && identity.ActorKind != "CI") || !strings.HasPrefix(identity.Caller.ID, "client:") || len(identity.Caller.ID) <= len("client:") {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	operationRequest := r.Clone(r.Context())
	operationRequest.Header.Del("Authorization")
	h.operation(w, operationRequest, identity)
}
