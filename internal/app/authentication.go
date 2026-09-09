package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BearerAuthenticator verifies opaque credentials for concurrent API requests.
type BearerAuthenticator interface {
	// Authenticate returns a stable principal and explicit operation permissions.
	// Invalid credentials return a safe non-nil error. Cancellation retains its
	// context error identity.
	Authenticate(context.Context, string) (httpapi.Identity, error)
}

// RunApprover authorizes one exact Run request for an authenticated principal.
type RunApprover interface {
	// ApproveRun returns nil only when all referenced immutable resources and the
	// candidate image are approved for the principal.
	ApproveRun(context.Context, string, run.Request) error
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
