# ADR-0025: Prepare scoped publication evidence

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Resolve registered evidence through authenticated request and native Run scope.
- Reuse the verified reader; fetch one bounded report and no candidate bytes.
- Keep preparation separate from retained replay, publication writes and delivery.

## Decision and consequences

Add callable evidence preparation in app composition with narrow scoped history
and artifact lookup ports and the existing ArtifactByteReader interface. Inputs
contain trusted provider/principal scope, scoped request, stable event identity/time
and registered artifact IDs. They cannot provide a native Run ID, URI or provenance.

Derive native Run from retained admission, require historical verification, check
all registered identities/metadata/formats and enforce the 1 MiB report bound before
object I/O. Use the configured objectstore reader for approved bucket and canonical
Run-path policy; public scoped Run IDs remain opaque and never select native paths.
Verify response metadata, bounded bytes, SHA-256 and body ownership there.
Independently check returned report bytes before returning owned materializer input.
Candidate evidence is a registered reference; never fetch its bytes here.

Reader absence, validation/mismatch, transient failure and cancellation retain safe
classifications. Backend/close diagnostics never reach callers. No transaction or
publication write occurs, and preparation alone validates no report verdicts: the
existing materializer still proves schema/provenance/outcomes against locked history.

This is a first-materialization step. Future orchestration must bypass preparation
and object reads for retained replay, using the existing ledger/materializer path.
No migration, wire contract, new dependency, discovery/listing, runtime activation,
scheduler or delivery is added. Reverting this callable boundary leaves data intact.
