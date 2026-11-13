# ADR-0044: Bind rerun raw approval to sealed execution

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Approve known rerun raw output under its retained source owner, not the synthetic child owner.
- Require frozen workload/schema, sealed OCI execution image and current reviewed producer metadata.
- Fail missing/changed evidence closed; normalization and live acceptance remain separate gates.

## Decision and consequences

M05-PR59h / issue #350 adds a raw provenance guard to capture-enabled runtime
composition. The child owner is an attribution/storage identity, not a registry
principal authorized to select another producer. Classify the immutable child
association first. Delegate only proven ordinary Runs to ordinary raw approval.
For known children, validate acceptance and exact child/owner/request, read the
seal, then approve the manifest against the original source-owner registry entry.

The registry checks the retained workload/contracts version and requires the
manifest's OCI producer image to occur in the sealed reusable Job, following the
existing execution-template image policy. Current reviewed producer name/version
and exact producer artifact must also match. Support raw v1 OCI images and raw v2
OCI artifacts; native/benchmark execution paths remain outside this consumer.
No schema fetch, object read, registration or Kubernetes I/O occurs in approval.

This is deliberately current producer approval, not a new historical metadata
snapshot. A withdrawn entry or changed name/version can refuse collection after
execution, using existing invalid-output classification. Never infer old metadata
or rewrite evidence to get approval. Immutable execution bytes/workload/schema
remain pinned; do not claim historical producer metadata survives arbitrary
registry changes. Historical collection does not reauthorize the actor's current
service grants or baseline approval; claim fencing remains with reconciliation.

Composition wraps only raw approval and preserves normalized/report/schema
capabilities. Byte checks and all collector provenance checks remain mandatory.
No public interface, persistence, dependency, migration, flag or deployment changes.
Normalization runtime/producer binding and full live rerun acceptance remain open;
public rerun serving stays disabled. Rollback retains seals and restores prior raw
approval, which cannot generally resolve synthetic child owners. No data repair or
backfill is needed.
