# Fixed-origin identity JWKS fetch

## TL;DR

- Issue #356 / M05-PR58c adds fixed-origin HTTPS fetching and strict RS256 JWKS decoding.
- Explicit root CAs, same issuer host/port, no proxies/redirects/discovery or credentials.
- Bound the attempt to three seconds, headers to 16 KiB, body to 64 KiB and document to 32 keys.
- This fetcher is not per-kid lookup; PR58d adds [bounded cache/refresh](identity-key-cache.md). Real provider qualification and serving remain separate.

## Trust and transport

`NewIdentityJWKSFetcher` accepts only operator-configured HTTPS issuer/endpoint
URLs without user info, query or fragment. Their host/port strings must match
exactly. It clones an explicit CA pool and constructs its own verifying TLS
transport, minimum TLS 1.2; it does not accept an arbitrary injected HTTP client.
System roots, if desired, must be supplied explicitly by trusted composition.
No token field or discovery response chooses a URL. No proxy environment,
Authorization, cookie jar, automatic redirects or compression is used.

Only HTTP 200 and JSON/JWK-set JSON media types are accepted. Content encoding,
oversized/invalid JSON and bad key sets fail unavailable without endpoint/backend
messages or partial keys. Caller cancellation/deadline is preserved; internal
attempt failures are unavailable. The owner releases idle connections through
`CloseIdleConnections` when retiring the fetcher.

## Key-set and timestamp profile

The existing duplicate/depth/UTF-8 scanner checks the entire bounded document.
The top-level object contains only `keys`; every record has bounded unique `kid`
and explicit key type/use/algorithm. Duplicate IDs across supported and ignored
keys refuse the entire set. Private/symmetric material is rejected. Non-target
keys are ignored; at least one RSA/sig/RS256 public key must remain.

Selected keys use canonical unpadded base64url unsigned `n`/`e`, with no leading
zero octets; the shared RSA checks require a positive odd 2048–8192-bit modulus
and supported odd exponent. Optional key operations must be exactly `verify`.
Selected-key fields are closed except the documented certificate metadata fields,
which are not used as trust anchors or fetched. The basis is [JWK](https://www.rfc-editor.org/rfc/rfc7517.html)
and [RSA JWK integers](https://www.rfc-editor.org/rfc/rfc7518.html#section-6.3);
this is a deliberately narrower candidate profile, not every legal JWK variant.

Each successful fetch returns independently owned keys and a timestamp captured
at the **successful attempt's start**, conservatively bounding trust age. The
fetch must still complete within its deadline and five-minute freshness bound;
zero/future-relative timestamps or an overlong attempt refuse. HTTP Date/cache
headers and cache reads never renew freshness. This start timestamp deliberately
avoids treating a long request or suspended process as a freshly obtained key set.

## Composition and evidence

`FetchIdentityKeys` receives no `kid` and does not implement `IdentitySigningKeys`.
PR58d provides the separate coalesced/rate-limited cache with bounded snapshot
absence, atomic retirement and expiry. Its real provider qualification and
per-issuer serving composition remain separate gates.
A one-connection transport is not that policy. No production composition, runtime
configuration, service mode or deployment is introduced; no dependencies added.

Real loopback TLS tests prove configured CA verification, redirect refusal,
response/size/encoding rejection, cancellation and separate mutable snapshots.
A fetched public key verifies a real synthetic RSA signature through PR58b.
Decoder and clock-regression tests exercise ambiguity/private material and stale
attempt refusal. These are not real Keycloak realm/JWKS rotation receipts.

Run `go test -vet=off -race -count=1 ./internal/authentication` and `make validate`.
No database or Kubernetes protocol changed. Rollback removes this uncomposed fetcher
and decoder without changing Static authentication or persisted grants/audit.
