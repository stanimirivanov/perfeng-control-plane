# ADR-0020: Build publications from retained analysis evidence

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Bind exact report/policy bytes and independently trusted provenance to retained admission.
- Reuse native report parsing and policy validation without mutable approval lookups.
- Construct owned publication claims; persistence and delivery remain separate.

## Decision and consequences

Add an offline builder in the existing integrationcontract edge. Callers supply
authenticated retained admission/native evidence, registered report/candidate
references and independently approved producer/bundle provenance. Schema validity
and a body-provided digest establish none of those authorities.

Verify exact bytes, native associations, policy coverage/outcomes, frozen baseline
and time ordering using existing native parsers and policy.VerdictApprover. Keep
valid late/inconclusive evidence; do not consult current eligibility or collapse
outcomes into overall PASS. Original report bytes are never reserialized or fetched.
Project only the native report reference's kind from normalized to analysis for
the separate publication family; identity/URI/hash/size and native APIs stay intact.

The caller supplies stable event ID/time for later immutable retention. No storage,
endpoint, worker, dependency or runtime activation is added. Subsequent publication
storage and runtime construction consume this shared builder before authenticated
delivery and Argus ingestion. Revert before those consumers requires no data change.
