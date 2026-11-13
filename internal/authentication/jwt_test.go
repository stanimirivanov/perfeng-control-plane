package authentication

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type identityKeysFunc func(context.Context, string) (IdentitySigningKey, error)

func (f identityKeysFunc) LookupIdentityKey(ctx context.Context, kid string) (IdentitySigningKey, error) {
	return f(ctx, kid)
}
func signIdentityJWT(t *testing.T, key *rsa.PrivateKey, header, payload string) string {
	t.Helper()
	input := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	hash := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
func jwtPayload(t *testing.T, claims SignedIdentity) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"iss": claims.Issuer, "sub": claims.Subject, "aud": claims.Audience, "azp": claims.AuthorizedParty, "typ": "Bearer", "iat": claims.IssuedAt.Unix(), "exp": claims.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestJWTIdentityVerifierChecksRealSignaturesAndAdmission(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	profile, claims, now := identityFixture()
	calls := 0
	verifier, err := NewJWTIdentityVerifier(profile.Issuer, profile.Audience, identityKeysFunc(func(_ context.Context, kid string) (IdentitySigningKey, error) {
		calls++
		if kid != "current" {
			return IdentitySigningKey{}, ErrUnauthenticated
		}
		return IdentitySigningKey{PublicKey: &key.PublicKey, FetchedAt: now}, nil
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token := signIdentityJWT(t, key, `{"alg":"RS256","typ":"JWT","kid":"current"}`, jwtPayload(t, claims))
	verified, err := verifier.VerifyIdentityCredential(context.Background(), token)
	if err != nil || verified.Subject != claims.Subject || !verified.AccessToken || calls != 1 {
		t.Fatal("signed token refused", err)
	}
	auth, err := NewIdentityAuthenticator(profile, verifier, identityPolicyFunc(func(context.Context, VerifiedIdentity) error { return nil }), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if value, err := auth.AuthenticateIdentity(context.Background(), token); err != nil || value.Actor.ID != claims.Subject || value.Caller.ID != "client:console-delegate" {
		t.Fatal("signed admission failed", err)
	}
}

func TestJWTIdentityVerifierRefusesMalformedOrSubstitutedTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"signature", "wrong-key", "none", "hmac", "header-duplicate", "claim-duplicate", "nested-duplicate", "jku", "jwk", "crit", "id-token", "issuer", "audience", "extra-audience", "date-string", "fractional-date", "missing-iat", "expired", "future-iat", "long-life", "actor-chain", "confirmation", "padded", "oversized", "unknown-kid"} {
		t.Run(mode, func(t *testing.T) {
			profile, claims, now := identityFixture()
			header := `{"alg":"RS256","typ":"JWT","kid":"current"}`
			payload := jwtPayload(t, claims)
			trusted := &key.PublicKey
			switch mode {
			case "wrong-key":
				trusted = &other.PublicKey
			case "none":
				header = strings.Replace(header, "RS256", "none", 1)
			case "hmac":
				header = strings.Replace(header, "RS256", "HS256", 1)
			case "header-duplicate":
				header = `{"alg":"RS256","alg":"RS256","typ":"JWT","kid":"current"}`
			case "claim-duplicate":
				payload = strings.Replace(payload, `"sub":`, `"sub":"other","sub":`, 1)
			case "nested-duplicate":
				payload = strings.TrimSuffix(payload, "}") + `,"ignored":{"a":1,"a":2}}`
			case "jku", "jwk", "crit":
				header = strings.TrimSuffix(header, "}") + `,"` + mode + `":"https://untrusted.example/keys"}`
			case "id-token":
				payload = strings.Replace(payload, "Bearer", "ID", 1)
			case "issuer":
				claims.Issuer += "/"
				payload = jwtPayload(t, claims)
			case "audience":
				claims.Audience = []string{"other"}
				payload = jwtPayload(t, claims)
			case "extra-audience":
				claims.Audience = append(claims.Audience, "other")
				payload = jwtPayload(t, claims)
			case "date-string":
				payload = strings.Replace(payload, `"iat":`, `"iat":"invalid","ignored":`, 1)
			case "fractional-date":
				payload = strings.TrimSuffix(payload, "}") + `,"nbf":1.5}`
			case "missing-iat":
				payload = strings.Replace(payload, `"iat":`, `"ignored-iat":`, 1)
			case "confirmation":
				payload = strings.TrimSuffix(payload, "}") + `,"cnf":{"jkt":"unqualified"}}`
			case "expired":
				claims.IssuedAt = now.Add(-4 * time.Minute)
				claims.ExpiresAt = now.Add(-time.Minute)
				payload = jwtPayload(t, claims)
			case "future-iat":
				claims.IssuedAt = now.Add(time.Minute)
				payload = jwtPayload(t, claims)
			case "long-life":
				claims.ExpiresAt = claims.IssuedAt.Add(6 * time.Minute)
				payload = jwtPayload(t, claims)
			case "actor-chain":
				payload = strings.TrimSuffix(payload, "}") + `,"act":{"sub":"unqualified"}}`
			}
			token := signIdentityJWT(t, key, header, payload)
			if mode == "signature" {
				parts := strings.Split(token, ".")
				parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
				token = strings.Join(parts, ".")
			}
			if mode == "padded" {
				token += "="
			}
			if mode == "oversized" {
				token = strings.Repeat("a", 16385)
			}
			verifier, err := NewJWTIdentityVerifier(profile.Issuer, profile.Audience, identityKeysFunc(func(context.Context, string) (IdentitySigningKey, error) {
				if mode == "unknown-kid" {
					return IdentitySigningKey{}, ErrUnauthenticated
				}
				return IdentitySigningKey{PublicKey: trusted, FetchedAt: now}, nil
			}), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if value, err := verifier.VerifyIdentityCredential(context.Background(), token); !errors.Is(err, ErrUnauthenticated) || value.Subject != "" {
				t.Fatal("untrusted JWT admitted", err)
			}
		})
	}
}

func TestJWTIdentityVerifierRequiresFreshTrustedKeysAndPreservesCancellation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stale", "future", "outage", "cancelled", "negative-key"} {
		t.Run(mode, func(t *testing.T) {
			profile, claims, now := identityFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := run.ErrUnavailable
			verifier, err := NewJWTIdentityVerifier(profile.Issuer, profile.Audience, identityKeysFunc(func(context.Context, string) (IdentitySigningKey, error) {
				fetched := now
				public := key.PublicKey
				switch mode {
				case "negative-key":
					public.N = new(big.Int).Neg(key.N)
				case "stale":
					fetched = now.Add(-5*time.Minute - time.Second)
				case "future":
					fetched = now.Add(time.Second)
				case "outage":
					return IdentitySigningKey{}, errors.New("secret key-store details")
				case "cancelled":
					cancel()
				}
				return IdentitySigningKey{PublicKey: &public, FetchedAt: fetched}, nil
			}), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				expected = context.Canceled
			}
			token := signIdentityJWT(t, key, `{"alg":"RS256","typ":"JWT","kid":"current"}`, jwtPayload(t, claims))
			value, err := verifier.VerifyIdentityCredential(ctx, token)
			// Exact sentinels forbid wrappers containing key-store details.
			if err != expected || value.Subject != "" {
				t.Fatal("key trust failure leaked detail or identity", err)
			}
		})
	}
}
