# ADR-0041: Recheck current rerun validation eligibility

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Recheck current source-service grants and exact approved baselines on runtime sealing and replay.
- Serialize eligibility withdrawal with capture using the existing SQL transaction.
- Preserve historical seals and keep dispatch fenced pending separate qualification.

## Decision and consequences

M05-PR59e / issue #344 extends [runtime validation](../rerun-persistence.md#current-validation-eligibility-m05-pr59e).
Acceptance and a valid worker lease alone do not establish current authority.
Before first capture or replay, lock the original actor's current read/rerun
grants and exact approved baseline records after the owned-child/lease lock.
Sort baseline locks by ID/version and retain all eligibility locks until capture
commits or rolls back. Reject withdrawal without writing evidence.

Reusing the existing grant reader preserves the service ownership boundary and
permission order. Exact JSONB snapshot equality prevents substituting a changed
record that happens to remain approved. No new public interface or schema is
needed. Historical reads and cancellation require immutable evidence even after
withdrawal, so do not impose this check on those paths or delete old seals.

This is point-in-time validation, not authorization for a later Kubernetes call.
PROVISIONING remains unavailable for known reruns until dispatch admission and
lease-qualified execution are separately proven. No public producer is enabled.
Rollback retains schema/evidence and the dispatch fence; repair forward rather
than bypassing checks to enable execution.
