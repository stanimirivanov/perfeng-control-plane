# ADR-0018: Retain native bindings with integration admission

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Capture reviewed native bindings in the admission transaction, with no backfill.
- Preserve unbound legacy rows and historical replay without current approval reads.
- Keep runtime callback adoption and publication delivery in later slices.

## Decision and consequences

Migration 0020 adds nullable native_binding to retained integration requests.
NULL means unavailable evidence, while an available binding has an explicit
nullable baseline. Immutable guards forbid mutation/augmentation, bound deletion
and table truncation. Original accepted context/run/disposition remain attached.

AdmitIntegrationResolved adapts the owned registry resolution to existing admission
locks/clock/replay behavior. A baseline must equal the one snapshot read through
the transaction's scoped FOR SHARE reader. Capture explicit native workload,
test/policy/run and approved baseline identity/artifact before committing Run,
disposition and event digest together. Failed capture rolls all writes back.
Historical reads never infer missing bindings or re-read mutable approval state.

Apply 0020 before deploying readers/writers. The metadata-only nullable expansion
does no backfill or heap rewrite; its CHECK scans the request table while ALTER
holds ACCESS EXCLUSIVE, so schedule the migration for its size. No new index or
grant is needed with the existing table-level SELECT/INSERT grants. Retain schema
on application rollback; legacy writers remain valid and create unbound rows.
The additive method is not wired to HTTP/runtime yet. Live CI verifies upgrade,
atomic replay/rollback, scope, retirement/reopen and guards; local checks compile
those tests without running a database. No live migration is executed here.
