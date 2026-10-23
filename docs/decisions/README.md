# Architecture decision records

## TL;DR

- Use an ADR for durable decisions affecting contracts, persistence, security,
  topology, ownership or cross-component behavior.
- Number ADRs sequentially and never reuse a number.
- Allocate from fresh repository state and renumber concurrent collisions.
- Accepted ADRs are historical records; supersede rather than rewrite them.
- Record alternatives, consequences, migration, operations and validation.

## When an ADR is required

Create an ADR when a decision materially affects public compatibility,
persistence or data meaning, security or authorization, service/repository/
deployment topology, foundational technology, or cross-component operational
behavior. Cheap local implementation choices do not need an ADR.

## Naming and lifecycle

Use a four-digit sequence and a short kebab-case name:

```text
0001-select-reconciliation-ownership.md
```

Statuses are Proposed, Accepted, Rejected, Deprecated or Superseded. Add every
ADR to the index. Do not rewrite an accepted decision or its consequences.

Immediately before creating an ADR, refresh the branch as the workflow allows
and inspect the index and directory. Allocate the lowest unused four-digit
number. If concurrent work collides, rename the later ADR to the next available
number and update its title, index and references. Published numbers remain
immutable.

## Template

```markdown
# ADR-NNNN: Decision title

- Status: Proposed
- Date: YYYY-MM-DD
- Milestone: MNN — Outcome
- Deciders:
- Supersedes:
- Superseded by:

## Context

What forces a decision now? Include constraints and quality attributes.

## Decision

State the decision and scope precisely.

## Alternatives considered

### Alternative

- Benefits:
- Costs and risks:
- Reason not selected:

## Consequences

### Positive

### Negative

### Neutral or follow-up

## Compatibility and migration

## Security and operations

## Validation

How will assumptions and consequences be verified?
```

## Index

| ADR | Decision | Status |
|:--|:--|:--|
| [0001](0001-use-expiring-native-lease-capabilities.md) | Use expiring capabilities for native execution ownership | Accepted |
| [0002](0002-isolate-native-worker-presence-by-api-generation.md) | Isolate Native Worker presence by API generation | Proposed |
| [0003](0003-version-native-lease-authority.md) | Version the shared Native Worker lease authority | Proposed |
| [0004](0004-separate-native-upload-signing-endpoint.md) | Separate the native upload signing endpoint from artifact reads | Proposed |
| [0005](0005-serialize-native-worker-discovery.md) | Serialize native discovery before presence exists | Proposed |
| [0006](0006-reserve-benchmark-targets-before-dispatch.md) | Reserve benchmark targets before dispatch | Proposed |
| [0007](0007-fence-and-verify-benchmark-cleanup.md) | Fence and verify benchmark cleanup before releasing a target | Proposed |
| [0008](0008-resolve-evidence-contracts-per-registry-entry.md) | Resolve evidence contracts per authorized registry entry | Proposed |
| [0009](0009-pin-performance-catalog-contracts.md) | Pin performance catalog contract conformance | Proposed |
| [0010](0010-publish-authorized-performance-catalog.md) | Publish authorized performance catalog snapshots | Proposed |
