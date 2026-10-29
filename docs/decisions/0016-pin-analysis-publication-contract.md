# ADR-0016: Pin the analysis publication contract

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Adopt the reviewed separate publication family without changing existing pins.
- Keep vendor schema validation outside transport/core values and grant no authority.
- Retain native bindings and build publications in later independently reviewed slices.

## Decision and consequences

Pin performance-analysis/v1 schema/example at contracts commit
`cd2442f433ce93d9a886c14ad6ff8aa8488a623b`, bundle 0.25.0, in an independent
snapshot lock component. Reuse the existing offline JSON Schema adapter and
bounded JSON tooling without a new dependency or broad validator refactor.
Separate transport DTOs avoid conflating an artifact claim with verified native
domain evidence or widening the existing closed IntegrationMessage union.

The metadata codec preserves nullable baseline and exact byte-count syntax,
rejects ambiguity/resource hazards and checks per-message relationships. It
cannot authenticate or prove retained admission/native/policy/artifact bindings.
Those duties belong to subsequent atomic retention and publication/delivery slices
before Argus M09-PR9d2 activation. Existing native/catalog/integration pins and
runtime behavior remain unchanged; no migration, endpoint or release is added.

Snapshot checks, round-trip/concurrency and rejection tests, denied external
resolution, architecture checks and the existing harness verify this boundary.
Reverting the codec before consumer adoption requires no data rollback.
