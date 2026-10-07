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
	nativeWorkerUploadGrantTTL     = 2 * time.Minute
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
	nativeworker.BaselineLeaseStore
	// RenewNativeBaselineLease fences timing and cancellation updates to a v2 attempt.
	RenewNativeBaselineLease(
		context.Context, string, nativeworker.RenewalRequest, time.Duration, time.Duration,
	) (nativeworker.Renewal, error)
	// ReleaseNativeBaselineLease relinquishes only the matching v2 attempt.
	ReleaseNativeBaselineLease(context.Context, string, nativeworker.ReleaseRequest) error
	// ReserveNativeBaselineUploadGrant creates an exact, lease-fenced v2 upload reservation.
	ReserveNativeBaselineUploadGrant(
		context.Context, string, nativeworker.UploadRequest, time.Duration,
	) (nativeworker.UploadReservation, error)
	// PrepareNativeBaselineArtifact selects immutable object facts before external verification.
	PrepareNativeBaselineArtifact(
		context.Context, string, nativeworker.ArtifactRegistrationRequest,
	) (nativeworker.ArtifactCandidate, error)
	// RegisterNativeBaselineArtifact accepts only the verified v2 object.
	RegisterNativeBaselineArtifact(
		context.Context, string, nativeworker.ArtifactRegistrationRequest,
		nativeworker.VerifiedArtifact,
	) (nativeworker.ArtifactReference, error)
	// ReportNativeBaselineTerminal accepts one retry-stable, lease-fenced v2 outcome.
	ReportNativeBaselineTerminal(
		context.Context, string, nativeworker.TerminalRequest,
	) (nativeworker.TerminalAcknowledgement, error)
}

// NewNativeWorkerAPIHandler composes worker authentication, durable presence,
// lease lifecycle, verified artifact publication and terminal reporting
// without sharing the user-facing identity.
func NewNativeWorkerAPIHandler(
	store NativeWorkerStore,
	authenticator NativeWorkerAuthenticator,
	resolver nativeworker.BaselinePlanResolver,
	uploadSigner nativeworker.UploadSigner,
	artifactVerifier nativeworker.ArtifactVerifier,
	acquisitionEnabled bool,
) (http.Handler, error) {
	if store == nil || authenticator == nil || resolver == nil || uploadSigner == nil ||
		artifactVerifier == nil {
		return nil, ErrInvalidConfiguration
	}
	acquirer, err := nativeworker.NewBaselineAcquirer(
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
	v2Store := nativeWorkerV2Store{store}
	renewer, err := nativeworker.NewRenewer(
		v2Store,
		nativeWorkerLeaseTTL,
		nativeWorkerCancellationGrace,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	releaser, err := nativeworker.NewReleaser(v2Store)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	uploadIssuer, err := nativeworker.NewUploadGrantIssuer(
		v2Store,
		uploadSigner,
		nativeWorkerUploadGrantTTL,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	artifactRegistrar, err := nativeworker.NewArtifactRegistrar(v2Store, artifactVerifier)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	terminalReporter, err := nativeworker.NewTerminalReporter(v2Store)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	authenticateWorker := func(ctx context.Context, token string) (httpapi.WorkerIdentity, error) {
		identity, err := authenticator.AuthenticateWorker(ctx, token)
		identity.AuthorizedToUseLease = identity.AuthorizedToAcquire
		identity.AuthorizedToAcquire = identity.AuthorizedToAcquire && acquisitionEnabled

		return identity, err
	}
	handler, err := httpapi.NewNativeWorkerHandler(
		store,
		authenticateWorker,
		acquirer.Acquire,
		renewer.Renew,
		releaser.Release,
		uploadIssuer.Issue,
		artifactRegistrar.Register,
		terminalReporter.Report,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return handler, nil
}
