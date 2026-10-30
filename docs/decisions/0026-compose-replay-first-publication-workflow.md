# ADR-0026: Compose replay-first publication workflow

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Check retained publication before preparing or reading any artifact objects.
- Reuse existing preparation, builder and atomic materializer for new publication.
- Keep canonical replay checks centralized, with no runtime/delivery activation.

## Decision and consequences

Add callable app coordination over existing scoped ports and the verified reader.
On retained evidence, decode closed wire claims and check independent scope and
requested report ID before deriving the native reference from retained metadata.
Public scoped Run IDs remain opaque. Delegate exact event/time/report replay to
the existing materializer without reader, candidate ID, separate admission/artifact
lookups or a new write in this layer.

Replay deliberately performs two scoped publication reads: one discovers retained
metadata, the materializer's read owns canonical replay validation. This avoids a
second verifier implementation or a cache/delegating-store abstraction. No object
I/O occurs, and returned bytes remain owned. Recovery from uncertain commit uses
the same stable event/time/report identity; it does not repeat the uncertain write.

On absence, prepare registered scoped evidence outside transactions and pass it to
the materializer. Frozen policy/provenance, exact report bytes, independent outcomes
and transaction-supplied history are verified by existing boundaries. Candidate ID
and configured reader are required only for new publication. Failures produce no
partial bytes, preserve classification/cancellation and hide backend diagnostics.

No migration, wire contract, new dependency, automatic artifact/event selection,
endpoint, scheduler, runtime activation or external delivery is introduced. Callers
still authenticate/configure namespaces and select stable identities. Revert this
callable composition without changing retained records.
