package authentication

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

const (
	identityLifetime          = 5 * time.Minute
	identityClockSkew         = 30 * time.Second
	maximumIdentityTokenBytes = 16 << 10
)

// SignedIdentity is a trusted verifier's signature-checked access-token result,
// not decoded browser input. AccessToken must reflect a qualified token-type
// check. The verifier owns algorithm, key origin/freshness and strict JWT parsing.
type SignedIdentity struct {
	Issuer, Subject, AuthorizedParty string
	Audience                         []string
	AccessToken                      bool
	IssuedAt, NotBefore, ExpiresAt   time.Time
}

// VerifiedIdentity keeps accountable actor and issuer-attested client distinct.
// It conveys no legacy principal, service permission or proof of network presenter.
type VerifiedIdentity struct {
	Actor, Caller serviceaccess.Identity
	ActorKind     string
}

// IdentityProfile is trusted configuration for one exact issuer/audience and
// client-to-actor-kind profile. Identity enrollment is checked separately.
type IdentityProfile struct {
	Issuer, Audience string
	Clients          map[string]string
}

// IdentityCredentialVerifier must verify signature and token type with admitted
// keys before returning claims. Invalid credentials return ErrUnauthenticated;
// unavailable trust returns run.ErrUnavailable. Implementations must be concurrent-safe.
type IdentityCredentialVerifier interface {
	VerifyIdentityCredential(context.Context, string) (SignedIdentity, error)
}

// IdentityPolicy rechecks current subject enrollment/kind and caller delegation
// on every invocation. Missing, disabled or kind-incompatible policy must deny;
// it must not substitute token roles for service grants. No decision is cached here.
// Implementations must be concurrent-safe and return classified safe errors.
type IdentityPolicy interface {
	AuthorizeIdentity(context.Context, VerifiedIdentity) error
}

// IdentityAuthenticator admits trusted verifier output under current identity
// policy. It intentionally does not implement the legacy HTTP authenticator.
type IdentityAuthenticator struct {
	profile  IdentityProfile
	verifier IdentityCredentialVerifier
	policy   IdentityPolicy
	now      func() time.Time
}

// NewIdentityAuthenticator owns a defensive configuration copy. Verifier,
// policy and clock must support concurrent calls. Construction performs no I/O.
func NewIdentityAuthenticator(profile IdentityProfile, verifier IdentityCredentialVerifier, policy IdentityPolicy, now func() time.Time) (*IdentityAuthenticator, error) {
	if !validIdentityIssuer(profile.Issuer) || !identityText(profile.Audience, 512) || len(profile.Clients) == 0 || len(profile.Clients) > 100 || verifier == nil || policy == nil || now == nil {
		return nil, run.ErrValidation
	}
	clients := make(map[string]string, len(profile.Clients))
	for client, kind := range profile.Clients {
		if !identityText(client, 249) || (kind != "HUMAN" && kind != "CI") {
			return nil, run.ErrValidation
		}
		clients[client] = kind
	}
	profile.Clients = clients
	return &IdentityAuthenticator{profile: profile, verifier: verifier, policy: policy, now: now}, nil
}

// AuthenticateIdentity applies exact profile and ADR 0046 time bounds, then
// rechecks policy without caching. Rejection returns no partial identity or
// backend details and never attempts legacy authentication. This is not a JWT
// implementation or admission of a particular Keycloak token configuration.
func (auth *IdentityAuthenticator) AuthenticateIdentity(ctx context.Context, token string) (VerifiedIdentity, error) {
	if err := ctx.Err(); err != nil {
		return VerifiedIdentity{}, err
	}
	if !identityText(token, maximumIdentityTokenBytes) {
		return VerifiedIdentity{}, ErrUnauthenticated
	}
	claims, err := auth.verifier.VerifyIdentityCredential(ctx, token)
	if err != nil {
		return VerifiedIdentity{}, identityError(err)
	}
	if err = ctx.Err(); err != nil {
		return VerifiedIdentity{}, err
	}
	kind, allowed := auth.profile.Clients[claims.AuthorizedParty]
	actor := serviceaccess.Identity{Issuer: claims.Issuer, ID: claims.Subject}
	caller := serviceaccess.Identity{Issuer: claims.Issuer, ID: "client:" + claims.AuthorizedParty}
	if !claims.AccessToken || claims.Issuer != auth.profile.Issuer || len(claims.Audience) != 1 || claims.Audience[0] != auth.profile.Audience || !allowed || !actor.Valid() || !caller.Valid() || !validIdentityTimes(claims, auth.now()) {
		return VerifiedIdentity{}, ErrUnauthenticated
	}
	value := VerifiedIdentity{Actor: actor, Caller: caller, ActorKind: kind}
	if err = auth.policy.AuthorizeIdentity(ctx, value); err != nil {
		return VerifiedIdentity{}, identityError(err)
	}
	if err = ctx.Err(); err != nil {
		return VerifiedIdentity{}, err
	}
	if !validIdentityTimes(claims, auth.now()) {
		return VerifiedIdentity{}, ErrUnauthenticated
	}
	return value, nil
}

func validIdentityTimes(claims SignedIdentity, now time.Time) bool {
	return !claims.IssuedAt.IsZero() && !claims.ExpiresAt.IsZero() && claims.ExpiresAt.After(claims.IssuedAt) && claims.ExpiresAt.Sub(claims.IssuedAt) <= identityLifetime &&
		!claims.IssuedAt.After(now.Add(identityClockSkew)) && !claims.IssuedAt.Before(now.Add(-identityLifetime-identityClockSkew)) && !claims.ExpiresAt.Before(now.Add(-identityClockSkew)) &&
		(claims.NotBefore.IsZero() || (!claims.NotBefore.After(now.Add(identityClockSkew)) && claims.NotBefore.Before(claims.ExpiresAt)))
}

func identityText(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return r <= 32 || r >= 127 })
}

func identityError(err error) error {
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, run.ErrUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	for _, denied := range []error{ErrUnauthenticated, run.ErrForbidden, run.ErrNotFound, run.ErrValidation} {
		if errors.Is(err, denied) {
			return ErrUnauthenticated
		}
	}
	return run.ErrUnavailable
}

func validIdentityIssuer(value string) bool {
	issuer, err := url.Parse(value)
	return err == nil && issuer.Scheme == "https" && issuer.Hostname() != "" && issuer.User == nil && !strings.ContainsAny(value, "?#") && len(value) <= 512
}
