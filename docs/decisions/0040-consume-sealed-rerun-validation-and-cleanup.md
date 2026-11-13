# ADR-0040: Consume sealed rerun templates for validation and cleanup

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Validate known reruns against exact retained trust pins and seal the reviewed Job.
- Recover cancellation only from the seal, independently from a withdrawn source entry.
- Keep new dispatch explicitly blocked until admission and lease checks qualify.

## Decision and consequences

M05-PR59d / issue #342 follows [sealed validation and cleanup](../rerun-persistence.md#sealed-validation-and-cleanup-m05-pr59d).
Compose the guard under the existing report-capture flag while retaining the base
registry for benchmark discovery. Ordinary Run resolution remains delegated.
Require exact source-owner registry context and frozen request/workload/policy/
report-runtime/schema identities for first validation; never replace an old seal
with a changed proposal.

Because first capture is a write, validation uses an explicit claim-aware resolver
capability and the runtime writer checks lease ownership/expiry and Run revision
inside the capture transaction. Ordinary JobResolver callers cannot seal a known
rerun without a claim. This avoids a stale worker pinning a new owner to its plan.

Cancellation may outlive source-entry approval, so use the stored reusable Job
without selecting current registry bytes. Missing/corrupt evidence is an error,
not proof that no Job exists. Existing leased cleanup controls still apply.
Refuse new rerun provisioning until current-admission/lease-qualified dispatch
is implemented. This staged safety fence is deliberately not a success stub;
the next dispatch slice must explicitly retire it with rejection/recovery tests.
No public producer or live execution qualification is enabled. Rollback retains
accepted evidence/seals and uses compatible cleanup workers; repair forward.
