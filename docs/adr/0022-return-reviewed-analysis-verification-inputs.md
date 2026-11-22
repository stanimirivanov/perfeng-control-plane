# ADR-0022: Return reviewed analysis verification inputs

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Return exact policy bytes, analysis producer and bundle version from one review.
- Validate the native policy pin and clone all caller-owned mutable evidence.
- Defer durable capture and runtime publication until historical authority exists.

## Decision and consequences

Add the inward `integration.AnalysisVerification` value to AdmissionResolution as
optional Verification evidence. The registry supplies it from the same uniquely
selected entry that resolves the request, workload and approved baseline. Do not
infer producer or bundle provenance from the incoming report, a global version
sentinel or a second current-registry lookup.

Validate bounded policy semantics, metadata name/version and exact SHA-256 against
the native request, plus producer shape and concrete bundle version. Validation is
consistency checking, not authentication or proof of historical review. Clone exact
policy bytes and nested producer data before returning; do not reserialize policy.
Existing principal, immutable pin, cancellation and baseline approval checks remain.
The request-only wrapper retains its result and error contract.

Nil Verification supports legacy/custom review implementations and grants no
fallback authority. Existing admission storage does not retain this new evidence;
atomic historical capture is the next prerequisite for publication composition.
Both that storage boundary and historical publication consume this shared value.
No migration, wire/HTTP shape, new dependency, worker or delivery is introduced.
Reverting before those consumers requires no persistent data change.
