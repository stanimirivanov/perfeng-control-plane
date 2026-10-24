# ADR-0011: Pin performance integration contract conformance

- Status: Proposed
- Date: 2026-10-24
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Pin integration 1.0.0 separately from existing native and catalog contracts.
- Keep transport models in core and JSON Schema validation in an edge adapter.
- Validate conformance before future admission and lifecycle consumers act.

## Context and decision

Shared integration requests and responses use a closed CloudEvents union with
immutable context pins. The existing native validator does not implement the
complete JSON Schema vocabulary needed by this protocol. Pin the schema,
OpenAPI and three exchange fixtures at contracts commit
`041273947703542f7fe22eab838c176df5517974` (bundle 0.24.0), with independent
component provenance and checksums in the existing snapshot lock.

Use `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3 (Apache-2.0) behind
`internal/integrationcontract`. This is the validator already selected by the
integration plan and consumer implementation. It supports the published schema
without an external process or a general CloudEvents SDK. Its Go 1.21 minimum
fits this repository; the runtime adds the validator while reusing the existing
`golang.org/x/text` dependency. Upstream test dependencies are not runtime code.
The pinned graph remains subject to the repository vulnerability/license gates.
On 2026-10-24 upstream was unarchived, last updated 2026-10-18, and the
[v6.0.3 release](https://github.com/santhosh-tekuri/jsonschema/releases/tag/v6.0.3)
was published 2026-08-06. Current vulnerability results remain a separate gate.

Compile once with format assertions and an empty scheme-loader registry.
Untrusted JSON remains bounded and duplicate-key checked before validation.
Keep model types and per-message relationships vendor-independent in
`internal/contract`; architecture tests classify the validator as an edge.

## Alternatives considered

- Extend the bespoke native validator: fewer dependencies, but implementing and
  maintaining the full schema semantics would duplicate an established library.
- Add a CloudEvents SDK: useful envelope utilities, but it would still require
  payload validation and adds abstractions unnecessary for this fixed protocol.
- Rely on Go decoding alone: smaller, but loses closed-union, format and numeric
  constraints required for interoperable requests and responses.

## Consequences and compatibility

This is an internal conformance boundary with no new HTTP routes, grants,
database migrations or admission behavior. Existing native/catalog pins remain
unchanged. A future validator replacement can preserve the small decode/encode
boundary and run the same portable fixtures. Invalid messages return a generic
error without reflecting sensitive input or backend diagnostics.

The runtime must still verify trusted identities, exact reviewed resource pins,
durable replay/conflict behavior, lifecycle transitions and cancellation. Wire
validation is deliberately insufficient for authorization or current approval.
The [delivery guide](../performance-integration.md) identifies the remaining
admission and lifecycle slices; no temporary runtime interfaces are installed.

## Operations, rollback and validation

Embedded compilation fails fast if reviewed artifacts are corrupt; runtime
message handling performs no resource fetches. Reverting this foundation before
its consumers merge changes no persisted state. Concurrent fixture round trips,
malformed/oversized/duplicate-key input, numeric/time compatibility and denied
external resolution tests verify the boundary. Snapshot checks, architecture
tests and `make validate` enforce provenance, dependency placement and the
supply chain.
