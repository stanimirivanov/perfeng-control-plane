# ADR-0028: Resolve scoped admission by native Run

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Resolve native Run IDs through independently established provider/principal scope.
- Restore original evidence using the existing request-read implementation.
- Preserve historical absence; add no publication authority or runtime activation.

## Decision and consequences

Reporting starts with a native Run, while request-linked publication needs the
retained integration identity. Add an inward read port carrying native Run ID,
configured provider and authenticated principal issuer/ID. PostgreSQL resolves
the identity through the existing unique principal/Run association; no migration
or index is needed. Both queries share the existing bounded operation context.

The second query reuses request restoration for owned native/verification evidence
and current Run state. Recheck the returned Run, provider and full principal scope
so a changed legacy mapping cannot escape visibility checks. Unknown, native-only,
unassociated and invisible Runs share NotFound with zero output. Cancellation and
safe storage failures retain existing classifications. This read is a projection,
not a transaction or execution/publication authorization decision.

No current registry lookup or retrospective evidence backfill occurs. Legacy nil
native/verification evidence and custom reviews without verification remain absent.
No wire schema, HTTP route, permission, lease, artifact access, publication write,
event reservation or worker is added. Future activation must still establish stable
publication identity and independently verify retained evidence. Reverting this
capability requires no data rollback.
