# ADR-0045: Bind rerun normalization to registered evidence

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Resolve and approve accepted-child normalization through its retained source owner.
- Require exact registered raw manifest/source references and frozen workload/schema compatibility.
- Keep current normalizer review explicit; public admission and full live acceptance remain separate.

## Decision and consequences

M05-PR59i / issue #352 wraps both normalization planning and normalized-manifest
approval under the existing report-capture flag. Classify the private immutable
child association first. Known children must match their accepted owner/ID/request
and every raw manifest/source reference must equal the registered child artifact.
Missing evidence is unavailable and substituted metadata conflicts; neither permits
ordinary fallback. Ordinary Runs retain their existing reviewed normalization path.

Use the original source-owner registry entry after verifying the frozen workload
and contracts version. Retain existing source-format selection, candidate approval,
normalizer producer/profile validation and output producer/source checks. The
collector still verifies object bytes, raw provenance and measurement windows;
existing Job fingerprint/identity checks and reconciliation lease fencing remain
mandatory. Do not rewrite the child principal or expose delegation via an API.

Planning and output approval share one guard in runtime composition. Preserve raw,
report and schema trust plus report execution capabilities. Construction performs
no reads or external calls. No new SQL write, schema, migration, dependency, flag,
public API or deployment is needed.

This is current normalizer/profile review, not a historical runtime snapshot.
Withdrawal, incompatible raw format, changed producer metadata or a conflicting
existing deterministic Job may refuse progress. This does not establish historical
normalizer/profile continuity across arbitrary configuration changes. Do not infer
old metadata, replace conflicting executions or weaken provenance to force success.
Public rerun serving and full live rerun acceptance remain unqualified. Rollback
retains all evidence but restores ordinary normalization lookup, which generally
cannot resolve synthetic child owners.
