# ADR-0009: Pin performance catalog contract conformance

- Status: Proposed
- Date: 2026-10-23
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Pin the catalog generation independently of existing API and evidence versions.
- Prove local validation and canonical digest compatibility before runtime exposure.
- Keep publication and admission in their owning later slices.

## Decision

Add the reviewed 0.24.0 performance-catalog/v1 schema and producer fixtures with
independent provenance. Extend the existing offline adapter for its exact schema
forms and restricted canonical resource digest, without adding dependencies or
relabeling existing API/evidence versions. This foundation is shared by catalog
publication (M09-PR3b) and integration request admission (M09-PR6).

## Consequences

The original catalog task exceeded its agreed size band when contract adaptation
and runtime behavior were combined. Conformance is an independently testable
foundation; the prepared endpoint work remains in issue #264. No HTTP, registry,
authorization, database or deployment behavior changes in this PR. Existing
schema generations retain their meaning; joint approval remains pending review.
