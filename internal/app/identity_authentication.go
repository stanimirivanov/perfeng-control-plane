package app

import (
	"context"
	"crypto/x509"
	"sync/atomic"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// NamedIdentityConfig is trusted private composition input, not token-derived
// discovery or a public serving toggle. Roots and client profiles are copied.
type NamedIdentityConfig struct {
	Profile      authentication.IdentityProfile
	JWKSEndpoint string
	Roots        *x509.CertPool
}

// NamedIdentityAuthentication owns one issuer's shared key cache and transport.
// Keep one instance per configured issuer per process, not one per request.
// It intentionally cannot authenticate as a legacy principal or grant service rights.
type NamedIdentityAuthentication struct {
	authenticator *authentication.IdentityAuthenticator
	cache         *authentication.IdentityKeyCache
	fetcher       *authentication.IdentityJWKSFetcher
	stopped       atomic.Bool
}

// NewNamedIdentityAuthentication composes the admitted adapters without I/O.
// The caller owns Stop; the borrowed store and clock must support concurrent calls.
// Construction is not admission of an actual Keycloak deployment/token profile.
func NewNamedIdentityAuthentication(config NamedIdentityConfig, store authentication.IdentityPolicyStore, now func() time.Time) (*NamedIdentityAuthentication, error) {
	policy, err := authentication.NewStoredIdentityPolicy(store)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	fetcher, err := authentication.NewIdentityJWKSFetcher(config.Profile.Issuer, config.JWKSEndpoint, config.Roots, now)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	cache, err := authentication.NewIdentityKeyCache(fetcher, now)
	if err != nil {
		fetcher.CloseIdleConnections()
		return nil, ErrInvalidConfiguration
	}
	verifier, err := authentication.NewJWTIdentityVerifier(config.Profile.Issuer, config.Profile.Audience, cache, now)
	if err != nil {
		cache.Stop()
		fetcher.CloseIdleConnections()
		return nil, ErrInvalidConfiguration
	}
	authenticator, err := authentication.NewIdentityAuthenticator(config.Profile, verifier, policy, now)
	if err != nil {
		cache.Stop()
		fetcher.CloseIdleConnections()
		return nil, ErrInvalidConfiguration
	}
	return &NamedIdentityAuthentication{authenticator: authenticator, cache: cache, fetcher: fetcher}, nil
}

// AuthenticateIdentity returns only current verified actor/caller identity.
// Shutdown refuses new operations and results still in flight after policy I/O.
// Cancellation retains priority; all failures return a zero identity and safe error.
func (runtime *NamedIdentityAuthentication) AuthenticateIdentity(ctx context.Context, token string) (authentication.VerifiedIdentity, error) {
	if err := ctx.Err(); err != nil {
		return authentication.VerifiedIdentity{}, err
	}
	if runtime.stopped.Load() {
		return authentication.VerifiedIdentity{}, run.ErrUnavailable
	}
	identity, err := runtime.authenticator.AuthenticateIdentity(ctx, token)
	if err != nil {
		return authentication.VerifiedIdentity{}, err
	}
	if err := ctx.Err(); err != nil {
		return authentication.VerifiedIdentity{}, err
	}
	if runtime.stopped.Load() {
		return authentication.VerifiedIdentity{}, run.ErrUnavailable
	}
	return identity, nil
}

// Stop is concurrent-safe and idempotent. It cancels shared refresh work and
// retires idle transport connections without closing the borrowed policy store.
// Cooperative in-flight fetches terminate within the cache's three-second budget.
func (runtime *NamedIdentityAuthentication) Stop() {
	if runtime.stopped.Swap(true) {
		return
	}
	runtime.cache.Stop()
	runtime.fetcher.CloseIdleConnections()
}
