# ADR-0017: Return owned native admission resolution

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Expose the exact native values already established by reviewed registry resolution.
- Return owned baseline evidence without granting durable admission/publication authority.
- Preserve the request-only resolver and defer atomic storage/runtime adoption.

## Decision and consequences

Add the inward `integration.AdmissionResolution` value and an additive registry
method returning request, target, explicit workload identity and optional approved
baseline snapshot from one review. Retain the existing request-only API as a
wrapper, sharing the same principal/pin/approval/error and cancellation checks.
Do not infer native workload identity from test ID or aliases, re-read a baseline
after verification, or reconstruct historical approval using current registry state.

Defensively clone baseline pointers and slices after validating its immutable pin.
Preserve empty versus null reason arrays because their encoded content hashes differ.
The only I/O remains the supplied scoped reader. Consumers that need atomic capture
must hold that reader's approval lock through admission commit. The returned value
alone is neither immutable storage nor authentication, publication or new execution
authority. Atomic retention and publication construction consume this shared result
in later slices; existing runtime review callbacks are unchanged here.

No dependencies, migrations, wire shapes or HTTP behavior change. Focused registry
tests verify exact authority, ownership, null baseline, zero output on failure and
legacy compatibility; architecture tests preserve inward dependencies. Reverting
before consumer adoption needs no data rollback.
