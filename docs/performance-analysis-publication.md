# Performance analysis publication boundary

## TL;DR

- Pin performance-analysis/v1 from bundle 0.25.0 and its read API from 0.26.0.
- The offline codec validates owned claims, never admission or artifact authority.
- Capture native bindings and reviewed verification durably before publication.

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
The offline builder below constructs a checked publication from retained evidence.
The publication ledger below retains one immutable association with durable replay
and conflict checks. Historical policy/provenance capture and callable composition
are described below; authenticated runtime delivery still precedes consumption.

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

## Offline publication builder

`integrationcontract.BuildAnalysisPublication` consumes retained admission/native
evidence, configured authenticated scope, independently trusted producer/bundle
provenance, registered report/candidate references and exact report/policy bytes.
Never derive these authorities from the incoming report. Storage origin/allowlists
and actual artifact authenticity remain caller duties; the builder fetches nothing.

It rejects missing historical bindings, altered/oversized bytes, substituted
scope/run/test/policy/producer/candidate/reference, missing policy rules and invalid
time ordering. Existing native parsing and policy validation check configured
sections/outcomes; report Unicode/numeric resource limits match the metadata codec.
No raw/normalized result bytes are read and no new statistics algorithm is added.
Late valid analysis remains usable after deadline/cancellation. Inconclusive and
SLO-pass/regression-fail evidence remain distinct through unchanged report bytes.

Native registered reports use kind normalized with format analysis-result/v1.
Only the publication reference projects kind analysis; ID/URI/hash/size stay exact.
The builder returns owned schema-validated claims and no partial output on error;
context cancellation stays classified. Calls are deterministic/concurrent-safe
with immutable inputs and use no current registry, baseline, clock or I/O.
Caller-provided event ID/time must be stored with the eventual publication and
reused on retry. Future storage/runtime must retain trusted policy/provenance
evidence rather than reconstructing it from a mutable registry or report claims.
Persistence, dispatch/delivery and Argus consumption remain
separate work. See [ADR-0020](decisions/0020-build-publications-from-retained-analysis-evidence.md).

## Immutable publication retention

After migration 0021, `integration.PublicationRepository` retains one publication
per authenticated principal/request issuer/request ID, with unique scoped source/event
identity. Callers supply stable event ID/time and a trusted offline verifier using
the builder and closed codec; it returns canonical bytes identical to the proposal.
Fetch exact evidence beforehand: no external I/O or current registry/baseline
approval reads may occur inside the transaction. Storage checks the retained native
binding, full original context, scoped Run and registered normalized report reference.
Legacy unbound admissions cannot publish. Verification failure rolls back completely.

Exact retries return original owned bytes without verification; different request
or event content conflicts. After uncertain commit, retry identically or read by
authenticated scope. Scoped reads recheck checksum and associations and return the
same not-found result for unknown, invisible or unpublished requests. Consumers still
decode these bytes with the publication codec. Cancellation/deadline do not erase
historical analysis. SQL guards forbid update, delete and truncate.

Apply 0021 and its SELECT/INSERT runtime grant before consumers. It creates an empty
table and indexes, takes brief referenced-table locks for foreign keys, and performs
no backfill or existing-row rewrite. Payloads add up to 64 KiB plus indexes/WAL per
published request. Roll back application code while retaining the additive schema;
do not rewrite migration history or delete published evidence. Runtime activation,
and authenticated delivery remain separate; historical verification retention is below.
See [ADR-0021](decisions/0021-retain-analysis-publications-atomically.md).

## Reviewed analysis verification inputs

`ResolveIntegrationAdmission` additionally returns optional Verification evidence:
exact policy bytes, expected report producer and concrete contracts version from
the same uniquely authorized entry. `integration.AnalysisVerification.Validate`
uses the existing bounded policy parser, checks metadata name/version and exact
SHA-256 against the native request, and validates producer/version shapes. It
performs no I/O, clock or current eligibility lookup. Validity alone proves no
authenticated source, historical approval or publication authority.

Returned policy bytes and nested producer evidence are independently owned.
Neither input claims nor a report can supply these authorities; registry-wide
`registry` version selection is resolved to the actual entry's concrete version.
Baseline-backed review still uses exactly one scoped locked read; SLO-only uses
none. Errors return zero resolution, cancellation stays classified, and the old
request-only wrapper keeps its behavior.

Nil Verification means absent legacy/custom review evidence, with no reconstruction
or fallback permission. The resolution itself remains an in-memory review result;
the atomic capture below supplies historical storage. Publication runtime remains
disabled. Resolution adds no runtime configuration or wire/HTTP contract changes.
Run focused integration/registry and architecture tests plus formatting/lint and
`make docs-check`; heavy full/race/live gates remain on CI. See
[ADR-0022](decisions/0022-return-reviewed-analysis-verification-inputs.md).

## Atomic verification retention

After migration 0022, new `AdmitIntegrationResolved` acceptances retain optional
Verification with native binding, Run, request and event in the same transaction.
The enabled registry path supplies it from the same review; invalid shape, policy
identity/version/hash or producer/version aborts the transaction. No extra approval
read, remote I/O or mutable registry lookup is added. Rejections retain no evidence.

Scoped `GetIntegration` and replay return owned `Record.Verification` and validate
its stored consistency. Exact policy bytes use base64 inside private persistence
JSON, separate from public wire messages. NULL means old/custom absence, never
SLO-only approval or permission to backfill; retries retain that absence without
review. SQL guards reject changes, augmentation, deletion and truncation, and check
bounded policy bytes against the retained native SHA-256. Valid data alone proves
no trusted origin beyond the admission review boundary.

Deploy 0022 before the binary. Adding the nullable column needs no backfill or heap
rewrite; CHECK validation scans existing rows under ACCESS EXCLUSIVE. Plan migration
lock time for table size. No index or privilege changes are needed. Each capture
adds up to 1 MiB policy bytes plus base64/provenance and TOAST/WAL overhead; the local
encoded snapshot bound is 2 MiB. Keep schema and evidence on application rollback;
old writers remain valid with NULL evidence. Do not reconstruct historical approval.
The callable materializer below consumes this evidence; runtime activation and
authenticated delivery remain follow-ups.
See [ADR-0023](decisions/0023-retain-reviewed-analysis-verification.md).

## Callable historical materialization

`integrationcontract.MaterializeAnalysisPublication` composes scoped history,
the offline builder and atomic retention through a narrow store interface. Supply
independently authenticated/configured provider/principal namespaces, scoped request,
stable event ID/time and prepared registered report/candidate references plus exact
report bytes. Callers enforce artifact storage origin/allowlists/authenticity first;
this boundary fetches nothing and exposes no current registry/approval port.

New publications require retained Verification. Its exact policy bytes, expected
producer and concrete bundle version exclusively supply builder authority; legacy
absence fails closed. Before retention, the no-I/O verifier rebuilds against the
transaction-supplied admission and checks the exact proposed claims. The result must
match the expected wire payload. Report bytes and independent outcomes are preserved.

Exact replay checks stored closed wire claims, independent scope, stable event/time
and exact report metadata, returning owned original bytes without report/candidate
re-verification or another admission load in this layer. Prepared bytes/candidate
may be absent on that path. Different event/time/report conflicts; scope mismatch
is forbidden. Already retained publications need no authority reconstruction. After
uncertain commit, retry identically or use the existing scoped publication read.
Errors return nil bytes and classified/redacted failures; cancellation is preserved.

No migration, runtime configuration, endpoint, scheduler or delivery is activated.
Runtime orchestration must select stable event identity/time and prepare evidence
before calling; fetch and delivery transport remain separate work. Revert callable
composition while retaining historical records. See
[ADR-0024](decisions/0024-materialize-historical-analysis-publications.md).

## Scoped publication evidence preparation

`app.PrepareAnalysisPublication` accepts authenticated/configured namespaces,
scoped request, stable event identity/time and canonical registered report/candidate
IDs. It derives native Run from scoped retained admission and requires historical
Verification before resolving references. Callers supply no URI, native Run ID or
producer/policy authority. Wrong scope, substituted IDs/Run, invalid formats and
reports over 1 MiB fail before object I/O. Historical cancellation does not erase
valid evidence or grant new execution authority.

Compose with the existing configured `objectstore.Reader` or S3 access reader:
approved bucket, canonical Run prefix, response metadata, bounded bytes, SHA-256
and response-body closure stay in that shared boundary. Preparation independently
checks returned length/hash and owns the exact output bytes. It reads one report;
the candidate is only a registered normalized-result reference. No object listing,
candidate fetch, policy lookup, transaction or publication write occurs.

Missing records/objects are not-found, invalid reference/location or corrupt bytes
are invalid publication evidence, backend failures are unavailable, and cancellation
stays classified. Errors return zero prepared input and no private storage details.
Prepared output still requires the materializer's report schema/provenance/outcome
and transaction-history verification; fetching alone grants no publication authority.

Preparation is for NEW materialization. The callable coordinator below first handles
retained replay through the ledger/materializer and bypasses preparation/object reads
on that path. Scheduling, stable event selection, runtime activation and authenticated
delivery remain separate. No migration or deployment configuration changes occur;
revert this callable boundary without changing stored evidence. See
[ADR-0025](decisions/0025-prepare-scoped-publication-evidence.md).

## Replay-first publication workflow

`app.PublishRegisteredAnalysis` joins preparation and materialization with the same
authenticated/configured scope, scoped request, stable event/time and report ID.
It checks the scoped ledger first. Retained closed claims must match independent
scope and requested report ID; their metadata supplies the native report reference.
The materializer then owns exact event/time/report replay checks and returns original
owned bytes. Candidate ID and object reader may be absent on this path, including
after uncertain commit, with historical verification absent or object access unavailable.
No separate admission/artifact lookup, object read or new retain occurs in this layer.

Replay intentionally uses two scoped publication reads: metadata discovery followed
by the existing materializer's canonical verification. This keeps replay logic in
one place without a cache or another store implementation. Changed event/time/report
conflicts and scope substitution is forbidden; failures never trigger preparation.

Only ledger not-found enters preparation. New publication requires the configured
verified reader and registered candidate ID, then uses the existing materializer's
frozen authority and transactional recheck. Readiness absence, cancellation and
backend failure remain classified/redacted with nil output. Retry uncertain commits
with identical stable identities; do not select new event/time on each attempt.

This is callable composition only: artifact/event selection, operator/runtime entry
point, scheduling and authenticated delivery remain separate. No migration or
deployment setting is added; revert this coordinator without altering retained data.
See [ADR-0026](decisions/0026-compose-replay-first-publication-workflow.md).

## Authenticated HTTP reads

Enabled reviewed integration exposes
`GET /integration/v1/requests/{requestId}/analysis?issuer=<request-issuer>`.
Percent-encode the opaque request ID and issuer once. Existing bearer ReadRun
permission is required; create permission alone grants no read. Authenticate and
authorize before lookup, require exactly one issuer query with no extra parameters,
and reject bodies/unsupported methods. Routing uses the escaped path, preserving
encoded /analysis inside an ordinary lifecycle request ID.

The adapter reads only principal/request-scoped original publication bytes and
validates the separate closed family, configured provider/principal namespaces and
full requested identity. Successful bytes are written unchanged as application/json.
It never invokes admission, current review, materialization, artifact fetch or writes.
All responses use Cache-Control: no-store. Unknown, invisible and unpublished requests
share 404/unknown_request/"Not Found" with identical body/headers. Missing credentials
return 401 with Bearer challenge; missing permission returns 403. Storage/timeouts or
corrupt/mis-scoped retained evidence return redacted 503 with Retry-After: 1.

`NewAPIHandler` requires the read capability for enabled integration; missing custom
storage capability fails startup. Disabled integration behavior stays unchanged.
Direct adapter users compose `NewAnalysisReadHandler` separately from lifecycle
handling and inject the offline analysis codec. Reads require the existing migrated
ledger/history tables through 0022; no new migration, token grant or configuration
is added. The API component pin advances to 1.1.0 at contracts commit
5bbeceec61f0ddd8922f27ceb6d4968cbdfef3ac while payload/native/catalog pins remain intact.

This exposes retained evidence only. Automatic materialization/event selection and
Argus polling/persistence remain separate; 404 is not a terminal or performance
verdict. Roll back read consumption/binary while preserving retained publications.
See [ADR-0027](decisions/0027-expose-scoped-analysis-publication-reads.md).

## Native Run admission lookup

`integration.RunAdmissionReader.GetIntegrationForRun` locates the original request
from a native Run ID and independently established provider/principal issuer/ID.
PostgreSQL uses the existing unique principal/Run association and shared request
restoration, with a visibility recheck after identity discovery. Unknown,
native-only, unassociated and invisible Runs all return NotFound with zero output.
The read has the existing operation timeout and preserves cancellation/errors.

Returned records own their evidence and include current Run state plus original
disposition/native/verification snapshots. Missing legacy or custom-review evidence
stays nil; no registry lookup or backfill occurs. This capability grants no
publication authority and activates no worker. Stable event selection, reporting
composition and publication recovery remain separate. No migration is needed.
See [ADR-0028](decisions/0028-resolve-scoped-admission-by-native-run.md).

## Scoped reporting publisher

`app.NewReportingPublisher` constructs the callable reporting gate adapter over
native admission lookup and the replay-first publication workflow. Configure the
provider/principal namespaces independently; the worker principal and native Run
resolve original retained request authority. Selected report/candidate IDs pass to
existing preparation and transaction verification; no public identity or historical
review is reconstructed from Run metadata or the current registry.

Native-only and unsupported legacy/custom history skip publication. Associated
scope/native request substitutions fail closed. Retained publication is checked
before missing-verification checks: replay recovers original event/time and needs
neither object access nor event allocation, including after uncertain writes.

For new publication the injected trusted `PublicationEventResolver` must retain the
same event/time across concurrent retries/restarts for the scoped request/evidence,
rejecting changed reservations. Its input has empty event/time; no ephemeral default
is provided. Nil verified reader supports replay only. This adapter does not activate
runtime composition or implement durable identity allocation; those remain separate
slices. No migration or protocol change is required for this callable composition.
See [ADR-0030](decisions/0030-compose-scoped-reporting-publication.md).

## Durable event reservations

`integration.PublicationEventStore.ReserveAnalysisPublicationEvent` supplies durable
event identity/time for the later reporting resolver. PostgreSQL retains one event
per scoped request and exact registered report/candidate IDs under the existing
request lock. Identical concurrent retries/reopen recover original values; changed
evidence conflicts, while unknown/provider/principal namespace mismatches are hidden.
New allocation requires reviewed native history and registered native artifacts.

An already verified publication returns its original event/time without creating a
reservation or inferring its candidate association. Conflicting report/reservation
history fails closed. Allocation uses database time after locking and an opaque
random ID. An uncertain commit requires identical retry; no external I/O occurs in
the transaction. The reservation itself never authorizes publication or sets a verdict.

Apply migration 0023 and trusted runtime SELECT/INSERT grants before consumption;
it adds an empty immutable table without rewriting/backfilling historical data.
Runtime resolver adaptation and activation remain separate. Preserve reservations
and publications on binary rollback; repair forward rather than reassign identity.
See [ADR-0031](decisions/0031-retain-publication-event-reservations.md).
