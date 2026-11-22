# ADR-0042: Dispatch reruns from claim-qualified seals

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Qualify private rerun provisioning by replaying the exact existing validation seal.
- Recheck current registry pins, SQL eligibility and claim ownership on each unbound attempt.
- Preserve deterministic recovery and grant-independent cancellation; public serving remains gated.

## Decision and consequences

M05-PR59f / issue #346 retires the provisional private provisioning refusal from
ADR-0040 only for claim-aware resolution. Reuse the claimed seal writer and its
current eligibility/lease/revision checks rather than inventing a persistent
permit. Allow the exact registry reviewer to inspect PROVISIONING, then require
an identical existing seal; it cannot reconstruct missing validation history.
Ordinary Runs keep their resolver behavior, and known-child errors never fall
back to it. No persistence format or public contract changes.

Provisioning carries the claim into resolution and retains its existing final
renewal/state/revision check before deterministic create/adopt and leased binding.
Bound executions bypass creation. Withdrawal denies new unbound attempts; pending
errors remain visible and cancellable. Cancellation uses the historical seal to
recover an ambiguous create even after withdrawal, without current registry review.

No SQL lock spans Kubernetes I/O. Current eligibility does not prevent revocation
or cancellation racing with an already in-flight request, nor prove exactly-once
execution. Existing bounded requests, attested deterministic identity and cleanup
remain mandatory. Full live rerun qualification, raw/normalization trust and public
identity admission are separate gates. No public producer is enabled.

See [operational semantics](../rerun-persistence.md#claimed-sealed-provisioning-m05-pr59f).
Rollback restores private provisioning refusal while retaining seals and compatible
cleanup workers; no down migration, deployment or flag mutation is required.
