# ADR-0047: Retain current identity enrollment and delegation

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Refine accepted ADR 0046 with durable, exact subject and caller bindings.
- Only trusted schema-owner provisioning writes policy; runtime gets SELECT only.
- Read both enabled flags on every admission; this grants no service permission.
- Identity serving and real Keycloak admission remain disabled and unqualified.

## Decision and consequences

Migration 0029 adds subject enrollment keyed by `(issuer, actor_id)` with immutable
HUMAN/CI kind, and delegation keyed by `(issuer, actor_id, caller_id)`. The latter
references enrollment and uses the signed-client `client:` namespace. The initial
ADR 0046 profile supports one issuer for actor and caller; cross-issuer delegation
is denied. No email, token role, implicit client-wide grant or historical backfill
creates enrollment.

A PostgreSQL adapter observes enrollment, kind and delegation in one statement.
The authentication-owned store interface avoids a PostgreSQL-to-authentication
adapter dependency. Absent or disabled policy denies; unavailable storage and
cancellation fail closed with safe errors. There is no policy cache. A committed
revocation is visible to the next statement, including concurrent readers. An
already-started statement may see its earlier snapshot: this is not a transaction
fence for a later service mutation. Serving integration must enforce current
identity policy alongside its operation-specific authorization boundary.

Use the existing trusted schema-owner provisioning boundary rather than inventing
a general access administration API in this slice. Runtime roles need only SELECT
on the two policy tables, never table ownership, DML, sequence or audit privileges.
Every insert/update writes an append-only audit row in the same synchronous
transaction, retaining database session_user, database time, exact identity, kind,
caller (NULL for enrollment), enabled flag and review reference. A review-only
update is also recorded. Delete/truncate and identity/kind relabeling are refused;
revoke by disabling. Schema owners/superusers remain trusted: they can bypass SQL
controls. This is not a tamper-proof audit against database administrators.

The operational tradeoff is manual reviewed SQL with a privileged operator, rather
than a delegated self-service administration surface. Separate operator roles or
API administration require their own bounded design. Enrollment and service grants
remain independent: admitting Maya or a CI subject grants no run or baseline right.

## Rollout, rollback and verification

Apply the additive migration through the existing checksum-ledger runner before
composing the policy adapter. Start empty/default-deny. Provision reviewed exact
bindings in an operator transaction and grant runtime SELECT explicitly; the
migration grants no runtime access. Do not change application-role ownership here.
Disable either binding to revoke; preserve rows and audit on rollback. Roll back
application code with expanded tables retained; no destructive down migration.

Live disposable PostgreSQL tests cover empty install, upgrade from 0028, exact
identity/kind/caller isolation, committed disablement, concurrent reads, audit,
immutable bindings and runtime write denial. Authentication tests cover safe
error normalization and cancellation. No issuer deployment, real-token receipt,
HTTP/public mode, BFF login or provider admission is established by this PR.
