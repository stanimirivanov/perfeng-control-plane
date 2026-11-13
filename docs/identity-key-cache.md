# Bounded identity key refresh and cache

## TL;DR

- Issue #356 / M05-PR58d adds the `IdentitySigningKeys` cache over admitted snapshot fetching.
- One shared three-second refresh, at most one attempt per ten seconds, per issuer cache/process.
- Retain at most 32 keys and no negative key IDs; original fetch timestamps never renew on reads.
- Five-minute freshness, atomic retirement and independent cancellation are enforced; serving and real Keycloak receipts remain unqualified.

## Lookup and negative policy

Use exactly one long-lived cache per admitted issuer/origin per process. A valid
known key returns an independently owned copy while its original snapshot remains
fresh. A missing/stale key joins one active refresh or starts one when the
ten-second attempt interval permits it. Each invocation waits at most once.
Concurrent callers share the same bounded fetch; a cancelled initiator/waiter
cannot cancel other callers' shared work. Fetchers must honor cancellation.

A recent successful snapshot's absence rejects missing IDs as unauthenticated
without retaining those IDs. This is snapshot-wide negative caching with **zero
per-kid entries**, rather than an attacker-expandable map. During the cooldown,
failure or stale/unavailable trust returns unavailable for unknown/stale keys.
Known still-fresh keys remain usable after a refresh failure; that failure never
renews their timestamps. New-key discovery may be delayed by the ten-second
interval and fetch budget: this is an explicit availability/security tradeoff.

## Publication, freshness and lifecycle

Validate/deep-copy each complete snapshot, then atomically replace the key set.
Removed keys disappear; do not union old and new sets. Empty, oversized, malformed,
stale, future or timestamp-regressing snapshots fail without replacing old trust.
No snapshot may contain more than 32 bounded key IDs and validated RSA public keys.
Both fetch results and returned lookup keys are independently owned.

The original successful-fetch timestamp is retained. Cache hits, misses, cooldowns
and failed refreshes never make it newer. Time is observed under the cache lock;
a backward observation refuses trust until time catches up, so previously expired
keys cannot become usable again. No network I/O occurs under that lock.

Shared work uses cache-owned cancellation plus a three-second deadline, not the
first request's context. The owner calls `Stop` to prevent further reads and cancel
in-flight work; stop is idempotent. The borrowed fetcher still owns its transport,
so retire its idle connections separately. No periodic background polling runs.
The refresh bound is per cache/process, not a fleet-wide rate limit or a limit on
external HTTP request concurrency. Creating a cache per token would violate it.

## Evidence and remaining gates

Tests use channels and a fake clock for coalescing, cancellation, stop, cooldown,
negative bursts, rotation, outages and clock rollback. Distinct synthetic RSA keys
verify new signatures and reject retired ones through the real JWT verifier. A
runtime-red regression showed that clock rollback could resurrect refused stale
trust before the observed-time guard was added. These are controlled adapter
receipts, not a real Keycloak/JWKS rotation or shared-service acceptance receipt.

Compose with the [fixed-origin fetcher](identity-jwks-fetch.md) and
[JWT verifier](identity-jwt-verification.md) only under reviewed per-issuer wiring.
No runtime configuration or HTTP mode is activated here. Current subject/caller
policy storage and
[private composition](identity-composition.md) now have synthetic integration evidence.
Real pinned issuer conformance and feature-gated serving remain separate work
under #356. No dependency, database, deployment or public API changes.
Run authentication race tests and `make validate`; no SQL/Kubernetes boundary changed.
Rollback of the cache slice preserves Static serving and grants; the
[composition guide](identity-composition.md) describes managed shutdown.
