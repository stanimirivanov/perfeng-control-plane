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

`ReportPolicyRegistry.ResolveIntegrationAdmission` returns an owned
`integration.AdmissionResolution` from the same unique reviewed entry and
principal-scoped baseline read as the existing request-only resolver. It carries
the native request/test/policy, execution target, explicit workload identity and
the exact approved baseline snapshot, or nil for SLO-only policy. Baseline
qualification pointers, dataset seed and lifecycle history are defensively copied.
Explicit empty reason arrays retain their representation and reviewed content pin.
Errors return zero output; no clock, second baseline lookup or new authority is
introduced. `ResolveIntegration` remains a compatible request-only wrapper.

This is current review evidence, not durable historical admission. The trusted
reader must hold its approval lock through the later admission transaction;
an ordinary standalone resolution provides no such lock or persistence guarantee.
Use the same result for atomic retention and publication construction, never a
later current-registry lookup. See
[ADR-0017](decisions/0017-return-owned-native-admission-resolution.md).

This callable boundary is shared by later native binding retention and publication
construction; it changes no admission response, HTTP route, database or worker.
Successful decoding proves no authentication, historical association or approval.
The existing integration message union stays closed and rejects this new family.

Atomic native capture is composed for enabled reviewed integration configurations.
Next build one immutable publication association using retained evidence,
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

## Atomic native capture

After migration 0020, `AdmitIntegrationResolved` captures native values with new
accepted admission. Its configured ResolvedReview uses exactly one scoped locked
baseline read for baseline-backed review and none for SLO-only. Returned baseline
bytes must match that locked snapshot and its approved pin. Invalid capture rolls
back Run, request and event writes. Replays return stored evidence without review,
including after baseline retirement; scoped `GetIntegration` includes `Record.Native`.
Nil Native means legacy/rejected absence; Native with nil Baseline means an explicit
SLO-only binding. Legacy writers stay valid and are never backfilled on retry.
Guards reject binding/parent mutation, augmentation, bound deletion and truncation.
Deploy the migration before the binary; retain it on rollback. CHECK validation
scans the request table under ACCESS EXCLUSIVE without a heap rewrite or backfill.
No new grants/routes are added. See
[ADR-0018](decisions/0018-retain-native-bindings-with-integration-admission.md).

## Runtime composition

Enabled reviewed integration configurations call ResolveIntegrationAdmission and
AdmitIntegrationResolved. The target and native evidence come from one review;
the supplied transaction reader keeps baseline approval locked through capture.
Missing native resolver/storage capability fails startup, with no fallback to
unbound admission. Disabled integration configurations keep their prior behavior.

Direct HTTP adapter callers choose exactly one of Review or ResolvedReview;
the latter requires NativeRepository. Auth, permissions and principal checks run
before either mode. GET/cancellation use the original store, and public responses
remain the closed integration/v1 messages without native metadata. SHA-256 source
requests still receive the retained unsupported_revision rejection.
Apply 0020 before deployment. Old requests remain unbound; rollback preserves
retained bindings while old writers create unbound requests. No publication/fetch
or Argus consumption is enabled. See
[ADR-0019](decisions/0019-compose-native-capture-for-authenticated-admission.md).
