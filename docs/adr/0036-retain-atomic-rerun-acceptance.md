# ADR-0036: Retain atomic rerun intent and acceptance before serving

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Stage private rerun acceptance, immutable lineage and audit in one transaction.
- Preserve actor-scoped retry identity independently from legacy create and caller rotation.
- Keep identity admission, public JSON and execution activation in their owning slices.

## Decision and consequences

Follow [rerun persistence](../rerun-persistence.md) for M05-PR50 / issue #334.
Use a dedicated append-only table with a versioned private receipt and references
to immutable native report inputs, rather than duplicating policy bytes or
reinterpreting legacy create bindings. Persist the child and its inherited service
binding in that transaction. Grant locks fence revocation; sorted approval locks
prevent new acceptance after retirement, while existing acceptance remains replayable.

Reserve actor/operation/key permanently in this slice; expiration never creates a
replacement child. Full receipt retention costs bounded bytes per intent; a later
purger needs a reviewed sealed-tombstone transition. Child native principals use
an internal actor-hash namespace to avoid granting legacy CI credentials ownership
of newly attributed human work. Qualify namespace reservation with service-mode
identity admission before use.

Reject acceptance assembled from current registry defaults, actor identity supplied
by UI state, separate best-effort audit writes and exposing a callable route before
workers honor the stored selection. This slice supports explicitly captured native
source inputs; publication-only history is not fabricated. The private acceptance
is not the public RerunReceipt. No contract snapshot, dependency or deployment is
changed. Rollback retains the expanded schema and accepted evidence; repair forward.
