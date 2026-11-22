# ADR-0024: Materialize historical analysis publications

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Compose publication construction and retention through narrow scoped ports.
- Use historical verification only and recheck transaction-supplied admission.
- Replay original bytes without prepared evidence; keep fetching/delivery separate.

## Decision and consequences

Add callable materialization in the existing integrationcontract edge using core
integration ports, with no concrete database or current registry dependency.
Inputs include independently authenticated/configured scope, scoped request,
stable event identity/time and prepared registered report/candidate evidence.
Storage origin/allowlists and artifact authenticity remain caller responsibilities.

New materialization requires retained native and verification authority. Use the
existing offline builder with frozen policy/producer/version, then rebuild against
the admission supplied by atomic retention. Reject any different proposal; never
perform external I/O in that verifier. Check the exact returned payload and own it.
Keep independent report outcomes by preserving exact artifact bytes and references.

For existing publications, validate stored wire claims and compare scope, event,
time and exact report metadata. Return owned original bytes without reloading
admission in this layer or re-verifying prepared report/candidate input. Changed
publication identity conflicts. This recovers uncertain commits through identical
retry; already retained publications remain readable even with legacy absence.

Failures return no partial bytes and redact backend diagnostics. No migration,
wire change, new dependency, endpoint, worker, fetching or delivery is enabled.
Runtime callers must still supply authenticated scope and stable event identity;
reverting this callable boundary leaves retained evidence intact.
