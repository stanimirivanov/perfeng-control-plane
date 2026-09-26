package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	nativeWorkerPresenceMaximumAge = 2 * time.Minute
	nativeWorkerLeaseTTL           = 2 * time.Minute
	nativeWorkerLeaseRenewAfter    = 40 * time.Second
	nativeWorkerCancellationGrace  = 30 * time.Second
	nativeWorkerCandidateLimit     = 100
)

// BearerAuthenticator verifies opaque credentials for concurrent API requests.
type BearerAuthenticator interface {
	// Authenticate returns a stable principal and explicit operation permissions.
	// Invalid credentials return a safe non-nil error. Cancellation retains its
	// context error identity.
	Authenticate(context.Context, string) (httpapi.Identity, error)
}

// NativeWorkerAuthenticator verifies credentials from the isolated worker audience.
type NativeWorkerAuthenticator interface {
	// AuthenticateWorker returns server-owned identity and policy. User API
	// credentials must not be accepted by this operation.
	AuthenticateWorker(context.Context, string) (httpapi.WorkerIdentity, error)
}

// RunApprover authorizes one exact Run request for an authenticated principal.
type RunApprover interface {
	// ApproveRun returns the server-owned execution target only when all referenced
	// immutable resources and the candidate image are approved for the principal.
	ApproveRun(context.Context, string, run.Request) (run.ExecutionTarget, error)
}

// NewAuthentication loads the mounted static credential document into
// immutable memory. Failures are classified without exposing paths or content.
func NewAuthentication(config AuthenticationConfig) (*authentication.Static, error) {
	if config.Validate() != nil {
		return nil, ErrInvalidConfiguration
	}
	authenticator, err := authentication.LoadStatic(
		os.DirFS(filepath.Dir(config.CredentialFile)),
		filepath.Base(config.CredentialFile),
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return authenticator, nil
}

// NewAPIHandler composes authentication, authorization and persistence at the
// HTTP boundary without binding a listener or accessing external services.
func NewAPIHandler(
	repository httpapi.Repository,
	authenticator BearerAuthenticator,
	approver RunApprover,
) (http.Handler, error) {
	if repository == nil || authenticator == nil || approver == nil {
		return nil, ErrInvalidConfiguration
	}
	handler, err := httpapi.New(repository, authenticator.Authenticate, approver.ApproveRun)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return handler, nil
}

// NativeWorkerStore contains the privileged persistence operations used by the
// independently authenticated worker boundary.
type NativeWorkerStore interface {
	httpapi.NativeWorkerPresenceStore
	nativeworker.LeaseStore
	nativeworker.RenewalStore
}

// NewNativeWorkerAPIHandler composes worker authentication, durable presence
// and lease acquisition and renewal without sharing the user-facing identity.
func NewNativeWorkerAPIHandler(
	store NativeWorkerStore,
	authenticator NativeWorkerAuthenticator,
	resolver nativeworker.PlanResolver,
) (http.Handler, error) {
	if store == nil || authenticator == nil || resolver == nil {
		return nil, ErrInvalidConfiguration
	}
	acquirer, err := nativeworker.NewAcquirer(
		store,
		resolver,
		nativeWorkerPresenceMaximumAge,
		nativeWorkerLeaseTTL,
		nativeWorkerLeaseRenewAfter,
		nativeWorkerCandidateLimit,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	renewer, err := nativeworker.NewRenewer(
		store,
		nativeWorkerLeaseTTL,
		nativeWorkerCancellationGrace,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	handler, err := httpapi.NewNativeWorkerHandler(
		store,
		authenticator.AuthenticateWorker,
		acquirer.Acquire,
		renewer.Renew,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return handler, nil
}
