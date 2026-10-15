# ADR-0008: Resolve evidence contracts per authorized registry entry

- Status: Proposed
- Date: 2026-10-15
- Milestone: M04 — Kubernetes Benchmark Decision Loop
- Deciders: Repository owner (pending review)
- Supersedes: None
- Superseded by: None

## TL;DR

- Opt into mixed bundles with `PERFENG_CONTRACTS_VERSION=registry`.
- Resolve one exact version from the authorized immutable registry entry before
  discovering or reading evidence, never from an artifact's own version claim.
- Keep numeric version configuration as the existing uniform startup guard.
- Apply the selection to raw, normalized, persisted raw-input and report parsing.
- Publish a corrected image before selecting the mixed M04 bundle; this change
  leaves environment promotion and deployment as separate gates.

## Context

The promoted M04 bundle preserves k6 checkout evidence at contracts `0.13.0`
and introduces contained kube-burner evidence at `0.22.0`. The registry loader
can represent those exact per-entry bindings, but process composition requires
one uniform version and gives that version to every artifact collector. Loader
validation alone therefore does not prove that the process can use a bundle.

## Decision

Add the explicit `registry` selection to the existing contracts configuration.
Startup still loads and validates every entry and referenced file. Numeric
`x.y.z` configuration keeps the existing whole-registry equality check; it is
not silently converted to mixed-version mode.

Define a consumer-owned contracts resolver at the reconciliation evidence
boundary. The immutable registry resolves using the authenticated principal and
the Run's exact test, catalogue, profile, environment and policy context during
COLLECTING, ANALYZING or REPORTING. Invalid context and an unmatched binding
remain distinct domain errors. Candidate admission is not reapplied to an
already accepted Run, consistent with existing evidence approval.

All collectors resolve once per operation before publication discovery or object
reads. The normalized collector uses the same selection for its normalized
envelope and persisted raw input. Versions are never inferred from evidence or
selected by trying several parsers. Existing shape, provenance, source binding,
baseline and verdict verification remain mandatory.

## Alternatives considered

- Relabel k6 evidence to `0.22.0`: changes attested producer output rather than
  fixing composition and requires an independent producer release.
- Remove k6 from the bundle: drops an existing supported decision loop.
- Remove the startup equality check alone: collectors still reject one entry.
- Accept any version declared by evidence: transfers trust to the producer.
- Deploy separate control planes: introduces avoidable operational ownership
  and topology changes for a registry distinction the process can resolve.

## Compatibility and migration

The setting is additive and explicit. Existing numeric settings retain their
startup constraint, including mixed-bundle rejection. Mixed mode retains exact
per-entry equality, not broad semantic-version compatibility or support for
arbitrary schema generations. Registry format, shared schemas, APIs, database
migrations, transaction boundaries, clocks and lease fences do not change.

## Security and operations

Registry mode is a deployment choice over reviewed immutable inputs, not an API
option or a runtime fallback. A missing/invalid version or unmatched context
fails closed before evidence I/O. Existing cancellation and stage-specific
error classification remain intact. Resolution is concurrent read-only work;
there is no mutable process-wide current version.

Publish and verify the new control-plane image, then select it and `registry`
mode through the environment repository's normal immutable promotion workflow.
Old images reject this new setting. Do not roll back only the binary while
retaining mixed configuration; restore a compatible image, configuration and
bundle together. Registry mode does not bypass M04's migration, credential,
RBAC or fresh admission requirements.

## Validation

Exercise actual filesystem bundle loading through startup with both k6 and
contained benchmark entries. Numeric modes must reject that bundle; explicit
registry mode must accept both entries while malformed bundles still fail.
Compose the real collectors, registry approvers and report verdict checker.
Verify both versions and reject cross-entry/unknown claims in raw, normalized,
persisted raw-input and report evidence. Test authorization before I/O,
concurrent entry resolution, invalid selections and cancellation, then run the
complete repository harness. Deployed acceptance remains an environment gate.
