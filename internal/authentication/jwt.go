package authentication

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// IdentitySigningKey is an independently owned RSA public key fetched from an
// admitted origin at FetchedAt. Token contents must not establish its provenance.
type IdentitySigningKey struct {
	PublicKey *rsa.PublicKey
	FetchedAt time.Time
}

// IdentitySigningKeys is prebound to the configured issuer and reviewed key
// origin. Lookup accepts only an opaque key ID, never an issuer or URL. Unknown
// keys return ErrUnauthenticated; unavailable trust returns run.ErrUnavailable.
// Implementations must be concurrent-safe and return independently owned keys.
type IdentitySigningKeys interface {
	LookupIdentityKey(context.Context, string) (IdentitySigningKey, error)
}

// JWTIdentityVerifier checks a narrow RS256 bearer profile using trusted keys.
// It performs no discovery or network I/O itself and is not an HTTP authenticator.
type JWTIdentityVerifier struct {
	issuer, audience string
	keys             IdentitySigningKeys
	now              func() time.Time
}

type identityJWTClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string `json:"azp"`
	Type            string `json:"typ"`
}

// NewJWTIdentityVerifier binds exact issuer/audience and a trusted key source.
// Header typ JWT and payload typ Bearer are this candidate profile's exact types;
// real Keycloak configuration remains subject to ADR 0046 qualification.
func NewJWTIdentityVerifier(issuer, audience string, keys IdentitySigningKeys, now func() time.Time) (*JWTIdentityVerifier, error) {
	if !validIdentityIssuer(issuer) || !identityText(audience, 512) || keys == nil || now == nil {
		return nil, run.ErrValidation
	}
	return &JWTIdentityVerifier{issuer: issuer, audience: audience, keys: keys, now: now}, nil
}

// VerifyIdentityCredential verifies syntax, signature, type, identity/time claims
// and trusted key freshness. Call IdentityAuthenticator afterward for current
// client/enrollment/delegation admission; signature verification grants no access.
func (verifier *JWTIdentityVerifier) VerifyIdentityCredential(ctx context.Context, token string) (SignedIdentity, error) {
	if err := ctx.Err(); err != nil {
		return SignedIdentity{}, err
	}
	kid, dates, ok := identityJWTEnvelope(token)
	if !ok {
		return SignedIdentity{}, ErrUnauthenticated
	}
	key, err := verifier.keys.LookupIdentityKey(ctx, kid)
	if err != nil {
		return SignedIdentity{}, identityError(err)
	}
	if err = ctx.Err(); err != nil {
		return SignedIdentity{}, err
	}
	if !freshIdentityKey(key, verifier.now()) {
		return SignedIdentity{}, run.ErrUnavailable
	}
	claims := &identityJWTClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return key.PublicKey, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithStrictDecoding(), jwt.WithIssuer(verifier.issuer), jwt.WithAudience(verifier.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(identityClockSkew), jwt.WithTimeFunc(verifier.now))
	if err != nil || parsed == nil || !parsed.Valid {
		return SignedIdentity{}, ErrUnauthenticated
	}
	if err = ctx.Err(); err != nil {
		return SignedIdentity{}, err
	}
	if !freshIdentityKey(key, verifier.now()) {
		return SignedIdentity{}, run.ErrUnavailable
	}
	value := SignedIdentity{Issuer: claims.Issuer, Subject: claims.Subject, AuthorizedParty: claims.AuthorizedParty, Audience: append([]string(nil), claims.Audience...), AccessToken: claims.Type == "Bearer", IssuedAt: dates["iat"], ExpiresAt: dates["exp"], NotBefore: dates["nbf"]}
	if !value.AccessToken || !identityText(value.AuthorizedParty, 249) || value.Subject == "" || len(value.Audience) != 1 || value.Audience[0] != verifier.audience || !validIdentityTimes(value, verifier.now()) {
		return SignedIdentity{}, ErrUnauthenticated
	}
	return value, nil
}

func freshIdentityKey(key IdentitySigningKey, now time.Time) bool {
	return validIdentityRSAKey(key.PublicKey) && !key.FetchedAt.IsZero() && !key.FetchedAt.After(now) && now.Sub(key.FetchedAt) <= 5*time.Minute
}

func validIdentityRSAKey(key *rsa.PublicKey) bool {
	return key != nil && key.N != nil && key.N.Sign() > 0 && key.N.Bit(0) == 1 && key.N.BitLen() >= 2048 && key.N.BitLen() <= 8192 && key.E >= 3 && key.E <= 2147483647 && key.E%2 == 1
}

func identityJWTEnvelope(token string) (string, map[string]time.Time, bool) {
	if !identityText(token, maximumIdentityTokenBytes) {
		return "", nil, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[2] == "" {
		return "", nil, false
	}
	header, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || !jsondocument.Valid(header, maximumIdentityTokenBytes) {
		return "", nil, false
	}
	fields, ok := jsondocument.ExactObject(header, "alg", "typ", "kid")
	if !ok {
		return "", nil, false
	}
	var alg, kind, kid string
	if json.Unmarshal(fields["alg"], &alg) != nil || json.Unmarshal(fields["typ"], &kind) != nil || json.Unmarshal(fields["kid"], &kid) != nil || alg != "RS256" || kind != "JWT" || !identityText(kid, 256) {
		return "", nil, false
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || !jsondocument.Valid(payload, maximumIdentityTokenBytes) {
		return "", nil, false
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil || claims == nil {
		return "", nil, false
	}
	for _, unsupported := range []string{"act", "may_act", "cnf"} {
		if _, exists := claims[unsupported]; exists {
			return "", nil, false
		}
	}
	dates := make(map[string]time.Time, 3)
	for _, name := range []string{"iat", "exp", "nbf"} {
		raw, exists := claims[name]
		if name == "nbf" && !exists {
			continue
		}
		var number json.Number
		if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &number) != nil {
			return "", nil, false
		}
		seconds, err := number.Int64()
		if err != nil || seconds < 0 || seconds > 253402300799 {
			return "", nil, false
		}
		dates[name] = time.Unix(seconds, 0)
	}
	return kid, dates, true
}
