# ADR-0035: Retain report-time comparison inputs before dispatch

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner and control-plane/Console maintainers
- Supersedes: None
- Superseded by: None

## TL;DR

- Capture trusted selection before dispatch; replay before current approval.
- Share retained inputs across Job generation, schema selection and attestation.
- Enroll only new opt-in Runs; preserve historical absence and existing publications.

## Decision and consequences

M05-PR47 follows contracts PR46 and storage PR44. Use a default-off capability
and immutable admission-time enrollment. Store versioned private input bytes
bound to the native principal/Run/candidate and exact accepted request. Preserve
policy/runtime/workload/schema pins, selected baseline artifacts and approved
snapshots, including explicit unresolved references. New core model/store
contracts keep reconciliation and SQL adapters from depending on each other.

The report resolver selects once and persists before dispatch. Persistence
locks Run then candidate/baselines, rechecking baseline approval before first
write. Replay skips current approval; changed evidence conflicts. No external
I/O runs inside these transactions. The same retained resolver supplies the
Job and output approver; the report codec also uses its retained version pin.
A Pod-template input digest prevents legacy Job adoption from fabricating
captured execution. Existing lease/revision and byte verification stay intact.

Reject reconstructing from today's catalog, selecting a newly approved baseline
on retry, retaining only policy IDs without bytes, and capturing the output
approver's later independently resolved view as if it were the dispatched plan.
Legacy runs remain unenrolled; their existing execution behavior is preserved.
Existing immutable publications/verification remain authoritative for their
complete associations and are not copied into another final-report table.
PR48 must prefer those proven associations before declaring history incomplete.

## Rollout, rollback and validation

Follow [the operational guide](../report-comparison-retention.md). Expand schema
and trusted worker privileges, stop old workers, then enable consistently for
future admissions. Mixed old reporting/new capture admission is unsupported.
Keep records/schema on rollback and repair forward; use capture-aware workers
to resume captured Jobs. No API snapshot upgrade, release, deployed endpoint,
new verdict or production migration is included.

Unit and disposable PostgreSQL tests cover immutable replay, missing references,
retirement, pre-capture revocation, clone ownership, schema/Job identity, scope,
concurrent writes, upgrade and SQL guards. These prove capture mechanics, not
public authorization, evidence-byte transfer or production disaster recovery.
