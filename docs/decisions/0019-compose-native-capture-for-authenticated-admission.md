# ADR-0019: Compose native capture for authenticated admission

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Enabled runtime admission uses the reviewed native resolver and atomic capture.
- Reject missing capabilities at startup; keep explicit legacy HTTP mode supported.
- Preserve authentication, wire payloads and historical unbound evidence.

## Decision and consequences

Compose ResolveIntegrationAdmission with AdmitIntegrationResolved through an
explicit resolved-review HTTP option. Exactly one review mode is required;
resolved mode requires native storage. Use the target returned by that same review,
without a second approval lookup. Authentication, permissions and principal binding
still precede admission; GET/cancellation retain the existing repository path.

This is behavior-adjacent: reviewed integration configurations now capture native
evidence for new acceptance and fail startup if their custom resolver/store cannot
support it. Disabled configurations remain unchanged, and direct legacy HTTP
options remain valid. No new endpoint, grant, wire schema or feature flag is added.

Apply migration 0020 before deployment. Replay never reconstructs missing legacy
bindings. Rollback retains schema/evidence; older writers create unbound rows.
Publication construction/delivery and Argus consumption remain separate work.
Focused composition/auth tests and live HTTP/PostgreSQL CI coverage verify routing,
retention, replay, source rejection, scoped reads and cancellation compatibility.
