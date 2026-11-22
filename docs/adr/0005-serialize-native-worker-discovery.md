# ADR-0005: Serialize native discovery before presence exists

- Status: Proposed
- Date: 2026-10-11
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Add an identity advisory lock before existing worker/Run/lease row locks.
- Use the same boundary for v2 presence, acquisition and lease mutations.
- Observe one fresh post-lock statement snapshot without changing records.
- No migration or authority recovery is introduced; all writers must upgrade
  before discovery-based recovery is enabled.

## Context

The contracts discovery operation must serialize with current-slot acquisition
and availability replacement even when presence is absent. Row locking alone
cannot lock a missing presence row. A pre-wait clock or snapshot could also
observe an expired or newly acquired attempt incorrectly. The existing worker
before Run row order and exact presence recheck remain essential fences.

## Decision

Use `pg_advisory_xact_lock(hashtextextended('native-worker/v2:' || workerID, 0))`
before native v2 presence, Run or lease row locks. The operation-specific prefix
separates this boundary from create-Run idempotency locking. Hash collisions
only cause extra serialization; queries retain full worker identity scope.
Use existing operation/lock timeouts and READ COMMITTED transactions.

Presence replacement now commits explicitly inside this boundary. Acquisition
and mutations retain their existing row lock order and post-lock clock reads.
Discovery takes the identity and presence locks, then queries one fresh
statement snapshot with one statement timestamp. It selects at most two current
slots to detect ambiguity, failing closed rather than choosing the latest.
Discovery performs no durable writes or external service calls.

## Alternatives considered

- Presence row locks alone preserve the existing order but cannot serialize
  first-report insertion against absent-state discovery.
- Serializable isolation needs retry policy and does not directly supply the
  bounded, shared worker-identity boundary.
- A new identity table adds schema/lifecycle overhead without new domain data.

## Consequences

Absent identities can be observed consistently without fabricated presence.
One worker's operations may wait on each other, bounded by the existing five
second lock timeout and fifteen second operation deadline. Unrelated identities
remain independent except theoretical hash collisions. Accepted withdrawal
fences old AVAILABLE observations but never revokes a lease committed earlier.
Snapshots can immediately become stale; they never permit evidence deletion.

## Compatibility and migration

Pin contracts 0.21.0 / Native Worker API 2.2.0. Existing payloads and persisted
meaning remain unchanged. No SQL migration or extra runtime grant is required.
Upgrade every writer before enabling discovery-based recovery clients; old
writers do not participate in the absent-identity advisory boundary. Rollback
disables new clients while retaining all stored records. ADR-0001's worker
before Run row order remains intact, with this additional identity prefix.

## Security and operations

Scope comes solely from worker authentication. Discovery selects no secret
capability, recipe or delivery material. Ambiguous capacity and malformed state
fail closed, and dependency errors remain classified and bounded. Manual
recovery requires process exclusion and acknowledged UNAVAILABLE intent.

## Validation

Verify domain/HTTP conformance, read-only durable state, worker isolation,
independent connection pools, terminal precedence, expiry, ambiguous capacity,
fresh post-wait observations, absent identity serialization and withdrawal
versus in-flight acquisition with disposable PostgreSQL and race detection.
