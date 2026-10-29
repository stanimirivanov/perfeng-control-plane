# Performance analysis publication boundary

## TL;DR

- Pin performance-analysis/v1 from contracts bundle 0.25.0 without runtime activation.
- The offline codec validates owned claims, never admission or artifact authority.
- Capture native bindings durably before building or delivering publications.

## Published contract and codec

The schema and example in `internal/contract/snapshot` are byte-for-byte copies
from perfeng-contracts commit `cd2442f433ce93d9a886c14ad6ff8aa8488a623b`.
The `performanceAnalysis` snapshot lock component records bundle 0.25.0 and exact
checksums while existing native, catalog and integration pins remain unchanged.
The Apache-2.0 snapshot license covers the new files.

`integrationcontract.DecodeAnalysisPublication` and `EncodeAnalysisPublication`
validate the separate closed publication family offline. They preserve all original
integration context, opaque scoped run, explicit native run/test/workload/policy
association, nullable baseline and immutable artifact claims. Transport DTOs in
`internal/contract` deliberately differ from verified domain artifacts. Test ID
need not equal workload ID; catalog aliases and revision strings establish neither.

Calls return owned values or zero output with redacted `ErrAnalysisPublication`;
unsupported generations also classify as `ErrAnalysisPublicationVersion`. Schema
compilation occurs once with no external resource loaders. Decode calls are
concurrent-safe; do not mutate outbound values concurrently during encoding.
No clock, network or storage access occurs.

The local metadata limit is 64 KiB, separate from the contract's 1 MiB maximum
analysis artifact size. Reuse bounded, unique-key UTF-8 JSON validation, additionally
rejecting unpaired escaped surrogates and numeric tokens over 128 bytes or exponents
outside -999 through 999 before schema arithmetic. Accepted numeric byte counts
retain exact syntax, including integer-valued exponents. Reject unknown fields,
invalid formats, mismatched native/artifact runs, baseline-null disagreement,
source/run issuer mismatch, URL credentials and inconsistent context/time claims.
Publication after the admission deadline remains valid; expiry grants no verdict.

## Remaining provider and consumer work

This callable boundary is shared by later native binding retention and publication
construction; it changes no admission response, HTTP route, database or worker.
Successful decoding proves no authentication, historical association or approval.
The existing integration message union stays closed and rejects this new family.

Next, capture native run/test/resource identities and the exact approved baseline
artifact atomically with accepted admission under existing approval locks. Preserve
legacy admissions without fabricating a historical mapping from current registry
state. Then build one immutable publication association using retained evidence,
verified analysis/policy bytes and admitted baseline checks. Define durable replay,
conflict, retention and authenticated delivery before enabling consumption.

Artifact URIs grant no fetch permission. Runtime code must authenticate scope,
enforce configured storage/origin policy and verify size/hash before parsing.
Quality, SLO, regression and uncertainty remain independent; publication or
COMPLETED never means overall PASS, blocking, execution or baseline approval.
Provider materialization/delivery precedes Argus M09-PR9d2 ingestion; Argus durable
decision evidence follows separately. No temporary runtime stubs are installed.

Revert this codec before its consumers merge without changing persisted state.
Run focused contract/integrationcontract and architecture tests, formatting/lint,
`make docs-check` and `make validate`. Heavy race/full/live suites remain CI work
on a constrained contributor host. See [ADR-0016](decisions/0016-pin-analysis-publication-contract.md).
