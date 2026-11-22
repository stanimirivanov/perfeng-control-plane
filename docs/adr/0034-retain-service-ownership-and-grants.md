# ADR-0034: Retain immutable service ownership and revocable grants

- Status: Proposed
- Date: 2026-11-12
- Milestone: M06 — Production Readiness
- Deciders: Repository owner and Console/control-plane maintainers
- Supersedes: None
- Superseded by: None

## TL;DR

- Expand storage before enabling service-mode authentication or HTTP checks.
- Bind Runs only through explicit reviewed service/creator associations.
- Use scoped authenticated database administration with atomic durable audit.

## Decision and consequences

M05-PR44 follows contracts PR #110. Migration 0025 retains immutable Run service
bindings and independent current grants, with false revocation tombstones.
Original native principal and creator remain separate from a human reader.
No implicit membership backfill or legacy-owner fallback occurs in service reads.
The core service-access vocabulary owns identities/rights; PostgreSQL owns rows,
constraints, administration and audit. There is no new public transport contract.

Initial grant/revoke administration uses an EXECUTE-only SQL function. Its
security definer checks exact session login/service assignments provisioned only
by the schema owner, fixes search path and qualifies relations. Database roles
supply trusted operator identity without choosing the later human/delegation
protocol. Reject caller-supplied administrator booleans, a new unauthenticated
HTTP admin endpoint, and application-only audit writes that can commit separately.

Follow [the operational guide](../service-access.md). Auditing and WAL flush
requirements are enforced in the write transaction. Identical concurrent state
changes deduplicate; opposite changes serialize. No automatic retries can restore
a revoked permission. Mutation authorization must later recheck current grants
inside its own transaction; a read observation is not a transferable capability.

## Compatibility, migration and validation

The expansion neither changes public API snapshots nor activates service mode.
Unbound rows stay principal-owned, and ordinary API/worker roles gain no new
write privileges. Empty/populated upgrades, scoped administration, direct-write
denial, immutable evidence, atomic rollback, concurrency and restart are tested
with disposable PostgreSQL. Runtime authentication, HTTP authorization and audit
read projections are not proven here. New read primitives consume independently
verified identities only after those future capabilities are admitted.

Retain expanded schema on binary rollback and repair forward. Database owners
and superusers remain trusted; this is not a policy against a compromised DBA.
