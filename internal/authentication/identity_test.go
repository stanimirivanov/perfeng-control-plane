package authentication

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type identityVerifierFunc func(context.Context, string) (SignedIdentity, error)

func (f identityVerifierFunc) VerifyIdentityCredential(ctx context.Context, token string) (SignedIdentity, error) {
	return f(ctx, token)
}

type identityPolicyFunc func(context.Context, VerifiedIdentity) error

func (f identityPolicyFunc) AuthorizeIdentity(ctx context.Context, value VerifiedIdentity) error {
	return f(ctx, value)
}
func identityFixture() (IdentityProfile, SignedIdentity, time.Time) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	profile := IdentityProfile{Issuer: "https://identity.example/realms/perfeng", Audience: "perfeng-control-plane", Clients: map[string]string{"console-delegate": "HUMAN", "ci-workflow": "CI"}}
	claims := SignedIdentity{Issuer: profile.Issuer, Subject: "subject-123", Audience: []string{profile.Audience}, AuthorizedParty: "console-delegate", AccessToken: true, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute)}
	return profile, claims, now
}

func TestIdentityAdmissionSeparatesActorCallerAndRechecksPolicy(t *testing.T) {
	profile, claims, now := identityFixture()
	var calls atomic.Int32
	var denied atomic.Bool
	auth, err := NewIdentityAuthenticator(profile, identityVerifierFunc(func(context.Context, string) (SignedIdentity, error) { return claims, nil }), identityPolicyFunc(func(_ context.Context, value VerifiedIdentity) error {
		calls.Add(1)
		if value.Actor.ID != "subject-123" || value.Caller.ID != "client:console-delegate" || value.ActorKind != "HUMAN" {
			t.Error("actor/caller substitution")
		}
		if denied.Load() {
			return run.ErrForbidden
		}
		return nil
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	profile.Clients["console-delegate"] = "CI"
	value, err := auth.AuthenticateIdentity(context.Background(), "opaque-to-this-layer")
	if err != nil || value.Actor.Issuer != claims.Issuer || value.ActorKind != "HUMAN" {
		t.Fatal("verified identity lost", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := auth.AuthenticateIdentity(context.Background(), "credential"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	denied.Store(true)
	if value, err = auth.AuthenticateIdentity(context.Background(), "credential"); !errors.Is(err, ErrUnauthenticated) || value != (VerifiedIdentity{}) || calls.Load() != 10 {
		t.Fatal("revocation cached or identity leaked", err, calls.Load())
	}
}

func TestIdentityAdmissionRejectsInvalidProfilesAndTimesBeforePolicy(t *testing.T) {
	for _, mode := range []string{"issuer", "audience", "extra-audience", "id-token", "caller", "subject", "expired", "future", "old", "long-life", "zero-expiry", "nbf", "nbf-after-expiry", "empty", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			profile, claims, now := identityFixture()
			token := "credential"
			calls := 0
			switch mode {
			case "issuer":
				claims.Issuer += "/"
			case "audience":
				claims.Audience = []string{"other"}
			case "extra-audience":
				claims.Audience = append(claims.Audience, "other")
			case "id-token":
				claims.AccessToken = false
			case "caller":
				claims.AuthorizedParty = "unregistered-client"
			case "subject":
				claims.Subject = ""
			case "expired":
				claims.ExpiresAt = now.Add(-31 * time.Second)
			case "future":
				claims.IssuedAt = now.Add(31 * time.Second)
				claims.ExpiresAt = now.Add(2 * time.Minute)
			case "old":
				claims.IssuedAt = now.Add(-6 * time.Minute)
			case "long-life":
				claims.ExpiresAt = claims.IssuedAt.Add(5*time.Minute + time.Second)
			case "zero-expiry":
				claims.ExpiresAt = time.Time{}
			case "nbf":
				claims.NotBefore = now.Add(31 * time.Second)
			case "nbf-after-expiry":
				claims.NotBefore = claims.ExpiresAt.Add(time.Second)
			case "empty":
				token = ""
			case "oversized":
				token = strings.Repeat("x", 16385)
			}
			auth, err := NewIdentityAuthenticator(profile, identityVerifierFunc(func(context.Context, string) (SignedIdentity, error) { return claims, nil }), identityPolicyFunc(func(context.Context, VerifiedIdentity) error { calls++; return nil }), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			value, err := auth.AuthenticateIdentity(context.Background(), token)
			if !errors.Is(err, ErrUnauthenticated) || value != (VerifiedIdentity{}) || calls != 0 {
				t.Fatal("invalid credential admitted or policy queried", err, calls)
			}
		})
	}
}

func TestIdentityAdmissionClassifiesTrustFailuresAndExpiryDuringPolicy(t *testing.T) {
	for _, mode := range []string{"invalid-signature", "verifier-outage", "policy-outage", "cancelled", "expires-during-policy", "ci"} {
		t.Run(mode, func(t *testing.T) {
			profile, claims, now := identityFixture()
			var verifierErr, policyErr, errorExpected error
			switch mode {
			case "invalid-signature":
				verifierErr = ErrUnauthenticated
				errorExpected = ErrUnauthenticated
			case "verifier-outage":
				verifierErr = errors.New("secret backend details")
				errorExpected = run.ErrUnavailable
			case "policy-outage":
				policyErr = errors.New("secret policy details")
				errorExpected = run.ErrUnavailable
			case "cancelled":
				verifierErr = context.Canceled
				errorExpected = context.Canceled
			case "expires-during-policy":
				errorExpected = ErrUnauthenticated
			case "ci":
				claims.AuthorizedParty = "ci-workflow"
			}
			auth, err := NewIdentityAuthenticator(profile, identityVerifierFunc(func(context.Context, string) (SignedIdentity, error) { return claims, verifierErr }), identityPolicyFunc(func(_ context.Context, value VerifiedIdentity) error {
				if mode == "ci" && value.ActorKind != "CI" {
					t.Error("CI classified as human")
				}
				if mode == "expires-during-policy" {
					now = now.Add(5 * time.Minute)
				}
				return policyErr
			}), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			value, err := auth.AuthenticateIdentity(context.Background(), "credential")
			// Exact sentinels forbid wrappers that could leak backend details.
			if err != errorExpected || (err != nil && value != (VerifiedIdentity{})) {
				t.Fatal("failure leaked identity or detail", err)
			}
		})
	}
}

func TestIdentityProfileRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	for _, mode := range []string{"http", "empty-query", "empty-fragment", "user-info", "audience-space", "client-kind", "client-id", "empty-clients"} {
		t.Run(mode, func(t *testing.T) {
			profile, claims, now := identityFixture()
			switch mode {
			case "http":
				profile.Issuer = "http://identity.example"
			case "empty-query":
				profile.Issuer += "?"
			case "empty-fragment":
				profile.Issuer += "#"
			case "user-info":
				profile.Issuer = "https://user@identity.example"
			case "audience-space":
				profile.Audience = "api audience"
			case "client-kind":
				profile.Clients["console-delegate"] = "ADMIN"
			case "client-id":
				profile.Clients[""] = "HUMAN"
			case "empty-clients":
				profile.Clients = nil
			}
			_, err := NewIdentityAuthenticator(profile, identityVerifierFunc(func(context.Context, string) (SignedIdentity, error) { return claims, nil }), identityPolicyFunc(func(context.Context, VerifiedIdentity) error { return nil }), func() time.Time { return now })
			if !errors.Is(err, run.ErrValidation) {
				t.Fatal("invalid trust configuration admitted", err)
			}
		})
	}
}

func TestIdentityAdmissionDoesNotQueryPolicyAfterCancellation(t *testing.T) {
	profile, claims, now := identityFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	auth, err := NewIdentityAuthenticator(profile, identityVerifierFunc(func(context.Context, string) (SignedIdentity, error) { cancel(); return claims, nil }), identityPolicyFunc(func(context.Context, VerifiedIdentity) error { calls++; return nil }), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if value, err := auth.AuthenticateIdentity(ctx, "credential"); !errors.Is(err, context.Canceled) || value != (VerifiedIdentity{}) || calls != 0 {
		t.Fatal("cancelled verification queried policy", err, calls)
	}
}
