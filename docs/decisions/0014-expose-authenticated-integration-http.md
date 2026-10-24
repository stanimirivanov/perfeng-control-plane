# ADR-0014: Expose authenticated integration admission and reads

- Status: Proposed
- Date: 2026-10-24
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Enable POST/GET integration routes only for reviewed bindings and durable stores.
- Reuse explicit Run permissions and verify the full principal claim before writes.
- Inject codec/review dependencies at composition; preserve adapter boundaries.

## Decision and compatibility

Compose the integration HTTP handler in `NewAPIHandler` when the immutable
registry reports at least one reviewed binding. Require `integration.Repository`;
enabled configuration with a memory-only backend fails startup. Inject schema
decode/encode functions from composition rather than coupling HTTP to another
edge adapter. Existing service metrics automatically cover the composed handler.
No new dependency, credential schema, environment variable or migration is added.

This is a behavior addition for configured bindings: `runs:create` authorizes
POST and `runs:read` authorizes GET, matching the underlying owned Run capabilities.
Catalog permission alone grants neither. Compare both configured principal issuer
and verified credential ID before calling admission, including expired/replayed
requests. Lookup is by credential principal plus request issuer/ID, never ID alone.

POST returns the immutable accepted/rejected disposition. GET returns the current
native revision projected into the published lifecycle vocabulary, or retained
rejection. Snapshot IDs are deterministic per provider/Run/revision. The source
algorithm unsupported by the native API produces a durable `unsupported_revision`
rejection. Unsupported wire generations retain generic codec error compatibility
while exposing an additional classification for the required HTTP error code.

## Operations, rollback and verification

Apply migration 0019 and runtime grants before enabling configured bindings.
The registry's provider/principal namespaces are trusted startup configuration;
payload fields cannot select them. Keep bodies bounded and errors closed/safe;
temporary errors return 503 with Retry-After. Disable bindings before binary
rollback, retaining request/event ledgers. Legacy configurations stay disabled.

Focused HTTP tests cover authentication, permissions, malformed/versioned input,
scope, retained responses and every lifecycle mapping. Composition tests prove
the injected review/baseline-reader path and reject missing durable storage.
Live HTTP/PostgreSQL tests verify replay, scoped snapshots and durable rejection.
Run `make validate` and `make db-validate`. Cancellation and recovery remain PR6d.
