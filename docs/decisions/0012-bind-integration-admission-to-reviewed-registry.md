# ADR-0012: Bind integration admission to reviewed registry entries

- Status: Proposed
- Date: 2026-10-24
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Add opt-in immutable bindings to the existing reviewed registry authority.
- Resolve exact authenticated claims and live baseline approval into Run input.
- Keep durable acceptance and lifecycle state in subsequent slices.

## Decision and alternatives

Extend registry entries with a reviewed request issuer, service, capability,
deployment, candidate source/image and evidence references. Operators verify the
association's provenance before mounting the bundle. Reuse existing principal
allowlists, catalog namespace/projection, candidate admission and baseline
compatibility validation. No dependency or domain schema change is required.

Inferring deployments from caller pins would turn untrusted claims into authority.
Remote proof fetching would introduce a new trust protocol, availability dependency
and resource-fetch surface. A separate mutable admission registry would duplicate
existing authority. None is introduced in this slice.

## Consequences, compatibility and operations

Legacy entries remain disabled for integration resolution. Ambiguity, changed
pins, incompatible or stale baselines fail closed. Backend failures return safe
unavailability, while cancellation remains observable. The resolver performs no
writes; live baseline reads occur outside transactions. Exact evidence order is
part of the immutable reviewed association. One binding selects one candidate per
entry; ambiguous duplicates require operators to disambiguate their pins.

Roll out the binary before adding optional `integration` configuration; remove
that member before reverting to an older binary. There are no HTTP routes,
permissions, database migrations or changes to existing native admission behavior.
Reviewing a bundle remains a trust obligation, not cryptographic proof generated
by this code. The [integration guide](../performance-integration.md) describes the
configuration and the remaining atomic admission/deadline/lifecycle work.

## Validation

Registry tests cover exact resolution, changed claims, principal isolation,
ambiguous/legacy bindings, startup configuration validation, defensive copies,
concurrent reads, cancellation and live baseline failure/retirement. Run
`make fmt` and `make validate`; no live database boundary changes here.
