# ADR-0039: Seal reusable rerun dispatch templates before side effects

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Persist one bounded immutable child template before Kubernetes side effects.
- Keep original acceptance immutable and distinguish template seals from execution hashes.
- Require separate current admission, lease and runtime integration before dispatch.

## Decision and consequences

M05-PR59c / issue #340 follows [template storage](../rerun-persistence.md#sealed-dispatch-templates-m05-pr59c).
Use a separate append-only child record instead of modifying the original rerun
receipt or trying to reconstruct a reusable template from a cluster UID/spec hash.
Capture only during VALIDATING before a bound execution; replay original bytes
before mutable-state checks and refuse changed proposals. Validate Job structure
through the existing Kubernetes policy without contacting the cluster.

The versioned private document is bounded to 1 MiB and hash-checked on read and
write. This adds per-child storage cost and an additive migration; it preserves
old Runs and does not infer template history. Trusted worker callers must still
prove reviewed provenance and lease/current-admission authority. No consuming
runtime adapter is activated in this slice. Validation and provisioning/recovery
will share the seal; raw/normalization qualification remains additional work.
Rollback retains accepted evidence and the new table, then repairs forward.
