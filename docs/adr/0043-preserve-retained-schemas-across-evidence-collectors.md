# ADR-0043: Preserve retained schemas across evidence collectors

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Use the retained/rerun-aware contracts selector for all evidence collectors.
- Apply the configured uniform-version restriction after selection, including retained pins.
- Fail known-rerun evidence errors before storage I/O; producer and runtime qualification remain separate.

## Decision and consequences

M05-PR59g / issue #348 extends the existing retained schema selector to raw and
normalized collection. Previously only report collection used that selector;
raw/normalized collection consulted the current registry, which could reject
valid retained-version evidence or use a different schema after restart.

Compose one selector for raw, normalized and report collectors. Prefer the
planner's ContractsVersionResolver capability when present, with the existing
explicit not-found fallback for ordinary uncaptured history. The rerun-aware
planner translates known missing evidence into unavailable, so known failures
cannot trigger that fallback. Apply artifactContracts outside the selection so
the configured uniform version restriction covers both retained and registry
results. Registry-version mode continues to allow supported per-Run versions.

This also tightens report behavior: a retained pin that conflicts with a fixed
configured version now fails before storage I/O rather than bypassing the uniform
restriction. Operators recovering a different retained pin must use an explicitly
reviewed compatible configuration; do not silently reinterpret old evidence.
Construction stays I/O-free, and no schema is fetched at runtime.

No public interface, persisted format, dependency, migration or deployment changes.
Producer approval, exact artifacts, byte checks and provenance stay independent;
this selector grants no new producer or execution authority. Raw/normalizer trust,
normalization runtime binding, public admission and full live rerun acceptance
remain unqualified. Rollback code retains immutable evidence but restores the
older composition gap, so public rerun producers must remain disabled.
