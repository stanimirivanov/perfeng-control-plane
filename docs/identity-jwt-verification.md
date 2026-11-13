# Explicit-key identity JWT verification

## TL;DR

- Issue #356 / M05-PR58b adds strict RS256 signature verification before identity admission.
- Use explicitly trusted, issuer-bound RSA key snapshots; token fields never choose a key endpoint.
- Reject duplicate JSON, unsupported JOSE headers/profiles and stale trusted keys; return no partial identity.
- PR58c adds [fixed-origin JWKS fetching](identity-jwks-fetch.md); refresh/cache policy, enrollment storage, real Keycloak receipts and HTTP activation remain unqualified.

## Dependency admission

`github.com/golang-jwt/jwt/v5` is pinned at **v5.3.1**, released 2026-01-28.
The tagged module declares Go 1.21 and no module requirements; this adds one
runtime module and two checksum entries, without transitive module upgrades.
Its [tagged license](https://github.com/golang-jwt/jwt/blob/v5.3.1/LICENSE) is MIT.
The [release](https://github.com/golang-jwt/jwt/releases/tag/v5.3.1) and
[tagged parser options](https://github.com/golang-jwt/jwt/blob/v5.3.1/parser_option.go)
were reviewed. Repository vulnerability and license gates remain required; these
observations are not permanent security clearance. The upstream [release history](https://github.com/golang-jwt/jwt/releases) records the earlier GHSA-mh63-6h87-95cp parser fix in v5.2.2; use the maintained v5 line rather than copying an old parser.

Use the library's signature verification and claims validation rather than a
custom production JWS cryptographic implementation. The standard library handles
crypto primitives but does not supply the JWT validation policy. The adapter
pins valid methods, issuer/audience, expiration, issuance checks, clock/leeway and
strict base64 decoding. It does not disable claims validation. The dependency is
inseparable from this behavioral/security slice; replacement is isolated behind
`IdentityCredentialVerifier`. No library runtime network feature is enabled.

## Candidate token and trusted-key profile

`JWTIdentityVerifier` consumes bounded compact JWS credentials. Before key lookup,
strict unpadded base64url decoding and the existing `jsondocument` scanner reject
invalid UTF-8, duplicate object keys at any depth and excessive nesting. JOSE
headers contain exactly `alg=RS256`, `typ=JWT` and a bounded `kid`; token-supplied
keys/locations, critical extensions and alternate algorithms are refused.

The candidate payload uses `typ=Bearer`, `azp`, standard issuer/subject/audience
and integer Unix-second `iat`/`exp` (optional `nbf`). Fractional, quoted, exponent
and out-of-range dates are refused rather than rounded; supported epochs are
0 through 253402300799. Non-authoritative additional claims may remain, but
`act`, `may_act` and `cnf` profiles are rejected: actor chains and sender binding
must not be silently ignored. Exactly one configured audience is required.
This shape is a candidate for the accepted Keycloak design, not a receipt from
an actual realm or support for arbitrary JWT/OIDC profiles. Real token-type and
client/subject-kind qualification remains mandatory under ADR 0046.

`IdentitySigningKeys` must bind the configured issuer to a separately reviewed
key origin. The only token-derived lookup argument is an opaque key ID; it is not
a URL, file path or authority to discover one. No lookup implementation is
provided here. Unknown keys return unauthenticated; unavailable trust returns a
safe unavailable result. Trusted snapshots contain independently owned RSA public
material (positive odd 2048–8192-bit modulus and supported odd exponent) and actual
fetch time. Zero/future fetch times and snapshots older than five minutes fail
unavailable. Freshness is checked before and after signature work. A source must
not refresh this timestamp merely because it served a cached key.

This enforces the age of supplied snapshots, not a five-minute network cache.
PR58c adds explicit same-origin TLS fetching and strict JWKS decoding. Refresh
limits, bounded/negative caching and real rotation remain later key-source work. The verifier honors cancellation and
sanitizes dependency/backend errors to existing safe sentinels.

## Composition and proof limits

Successful verification returns signature-checked claims only. Pass them through
[identity admission](identity-admission.md) for current allowlisted client and
subject-kind/delegation policy. Neither operation grants service access, provides
a legacy principal nor proves which process presents the bearer. No method is
wired into HTTP serving, and failed verification never falls back to Static auth.

Tests sign synthetic tokens with independent standard-library RSA primitives and
exercise the real library verifier plus admission. They cover signature/key
substitution, none/HMAC, duplicate claims/headers, unsupported key/header/profile
claims, issuer/audience/type/time failures, freshness and cancellation. This is
cryptographic adapter evidence, not real Keycloak issuance, JWKS refresh, current
policy persistence or browser/serving qualification.

Run `go test -vet=off -race -count=1 ./internal/authentication` and `make validate`
with the pinned toolchain. No SQL or Kubernetes protocol changed. Rollback removes
the uncomposed adapter/dependency while preserving Static authentication and all
persisted grants/audit. Issue #356 remains open until its real-provider and serving
acceptance gates pass.
