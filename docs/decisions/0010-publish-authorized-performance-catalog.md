# ADR-0010: Publish authorized performance catalog snapshots

- Status: Proposed
- Date: 2026-10-23
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Publish contract 0.24.0 safe metadata from reviewed principal-scoped registry entries.
- Require explicit publication metadata and a separate read permission.
- Bound immutable ephemeral snapshots; retain live admission as execution authority.

## Decision

Add GET /v1/performance-catalog without changing existing API generations or
per-entry evidence contract versions. Pin the catalog schema and interoperability
fixtures separately. Preserve native identity in its configured issuer namespace,
join exact principal-scoped baseline reads, and omit unsupported selections.
Use a bounded one-minute in-memory snapshot for pagination, reauthenticate every
page and require clients to restart on expiry/restart. No database migration or
new dependency is needed. Failure never publishes a partial or falsely empty view.

## Consequences

Registry and credential changes are opt-in and follow compatible binary rollout.
An older frozen page may still contain a subsequently retired baseline; new
publication and execution admission read current state. Catalog snapshots grant
no execution authority. Provenance of new owner-route references remains a
reviewed bundle obligation. Image publication/promotion and Argus synchronization
are separate work; joint approval of this proposed decision remains pending.
Snapshot caches are process-local; multi-instance enumeration requires instance
affinity. This PR makes no deployment routing or shared-cache changes.
