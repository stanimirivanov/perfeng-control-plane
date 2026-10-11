# Performance control plane

## TL;DR

- This repository owns authenticated run coordination, durable lifecycle state,
  reconciliation, baselines, artifact registration and control-plane runtime
  composition.
- It does not own workload execution code, statistical analysis, shared
  contract definitions or environment deployment composition.
- PostgreSQL provides durable state; reconciliation uses leases, revisions and
  persisted Kubernetes execution identity for restart safety.
- External HTTP, JSON, registry, object-store and Kubernetes data is validated
  before domain policy uses it.
- Start with the [documentation map](docs/README.md), run `make bootstrap` once,
  use `make verify` during development, and run `make validate` before review.

Go implementation of the candidate run-management API and lifecycle from
performance-platform proposal sections 12-13. The control plane owns run
coordination, not load generation or statistical decisions.

## Current scope

- Authenticated Run and exact-version baseline administration handlers.
- Strict declarative request validation against the pinned contract.
- Atomic, principal-scoped idempotent creation with 24-hour retention.
- Original acceptance replay, current-state reads and dispatch-aware cancellation.
- Revision-checked worker transitions, terminal-state and failure-code rules.
- In-memory repository for bounded tests/development, with concurrency tests.
- PostgreSQL repository, transactional migrations and immutable artifact references.
- Principal-scoped, restart-safe worker and HTTP discovery of persisted artifact references.
- Durable worker claims, lease renewal/recovery and fenced lifecycle updates.
- Duplicate-safe Kubernetes Job creation and adoption boundary.
- Identity-checked Kubernetes Job observation and cancellation requests.
- Lease-fenced, restart-safe persistence of Kubernetes execution identity.
- Bounded reconciliation attempts with lease renewal and cooperative shutdown.
- Explicit lifecycle decisions for identity-checked Kubernetes Job observations.
- Lease-fenced reconciliation of persisted executions and owned-Pod stop checks.
- Bounded provisioning through an injected approved-template resolver.
- Exact execution-template resolution from the immutable runtime registry.
- Validated lifecycle entry and explicit routing for every active Run state.
- Retry-safe registration of verified raw artifacts before analysis.
- Restart-safe orchestration of normalization through an injected executor.
- Approved normalization-plan resolution for k6 and Playwright raw evidence.
- Deterministic restricted analysis Job construction from approved plans.
- Deterministic restricted report Job construction from trusted policy and baseline plans.
- Inert composition of Kubernetes analysis and reporting lifecycle executors.
- Restart-safe report generation and durable completion through an injected executor.
- Duplicate-safe Kubernetes normalization Job creation and phase mapping.
- Duplicate-safe Kubernetes reporting Job creation and phase mapping.
- Bounded verification of immutable objects from approved S3 locations.
- AWS SDK for Go v2 adapter with safe S3 error classification.
- Stable-key raw-manifest discovery from verified S3 reference metadata.
- Stable-key normalized-manifest discovery from verified S3 reference metadata.
- Stable-key analysis-report discovery from verified S3 reference metadata.
- Consistent S3 artifact-access composition from one client and storage policy.
- Strict, secret-safe runtime configuration for S3 artifact access.
- AWS SDK client construction through the standard credential provider chain.
- Pinned, inert composition of raw, normalized and report evidence attestation.
- Secret-safe PostgreSQL and bounded HTTP server runtime composition.
- Strict mounted authentication configuration and inert HTTP API composition.
- Mounted reviewed-registry loading cross-checked with artifact contracts.
- Strict loading of reviewed analysis and reporting Job templates.
- Structured reconciliation failure reporting without arbitrary error text.
- Fail-fast aggregate loading of the complete environment-sourced startup policy.
- Ordered startup resource acquisition with idempotent, secret-safe database cleanup.
- Complete inert composition of the authenticated HTTP and reconciliation runtime.
- Signal-aware process entry point with structured, value-free lifecycle reporting.
- Unauthenticated liveness/readiness endpoints and fixed-cardinality Prometheus metrics.
- Authenticated Native Worker v2 presence with strict contract validation,
  server-owned qualification and last-seen PostgreSQL state.
- Authenticated native lease acquisition with reviewed capability matching,
  database-fenced ownership and header-only bearer capability delivery.
- Database-fenced native lease renewal with monotonic phase progression and
  cancellation deadlines, exposed through the authenticated Native Worker API.
- Idempotent native lease release with durable reason fencing and atomic
  cancellation completion.
- Fenced, retry-safe persistence of native artifact upload declarations with
  server-selected attempt-scoped object keys and no delivery credentials at rest.
- Secret-safe S3 SigV4 presigning for exact native artifact PUT requests,
  composed only after the durable upload reservation commits.
- Authenticated native upload-grant HTTP delivery with strict request,
  capability, error and no-store response handling.
- Authenticated native artifact registration with streaming object verification,
  post-verification lease fencing and retry-stable PostgreSQL identity.
- Lease-fenced native terminal reporting with complete-evidence enforcement,
  exact duplicate acknowledgement and atomic Run handoff or failure.
- Bounded worker and in-cluster Kubernetes client/controller runtime composition.
- Cross-validated reconciliation-stage timing configuration.
- Strict, generation-aware parsing and structural validation of raw-result/v1
  and raw-result/v2 manifests.
- Composable verification of manifest provenance and every declared raw object.
- Exact v1 and v2 raw-manifest provenance approval from the immutable runtime registry.
- Strict parsing and internal-consistency validation of normalized-result envelopes.
- Composable verification of normalized output bytes, sources and provenance.
- Exact normalized-result producer approval from the immutable runtime registry.
- Strict parsing and internal-consistency validation of analysis-result reports.
- Composable verification of report output bytes, candidate binding and approval.
- Exact per-rule report binding to run-pinned policy bytes, authorized producer and approved baselines.
- Strict performance-policy parsing and independent report-verdict verification.
- Principal-scoped immutable registry for Run admission and report trust resolution.
- Bounded strict loading of reviewed registry manifests, exact policies and Job templates.
- Versioned baseline candidates with revision-checked qualification, approval and retirement.
- Principal-scoped PostgreSQL baseline storage backed by completed, registered evidence.

The control-plane process is packaged as a minimal, non-root Linux AMD64 image,
with manual, main-branch-only publication to GHCR. The same image includes the
administrative `/usr/local/bin/migrate` command for an explicitly authorized
migration Job and `/usr/local/bin/validate-registry` for offline bundle checks;
its default entry point is `/usr/local/bin/control-plane`. This repository does
not yet own a Kubernetes deployment. Reconciliation
stages start only after complete configuration, mounted inputs and external
dependencies have been validated. Catalogue and
environment publication verification, registry source attestation and refresh,
and analysis/report input and output transfer are still missing from the
repository-owned deployment. Tests use synthetic contract fixtures, not approved
deployable resources.

The in-memory adapter loses runs and idempotency bindings on restart. It cannot
meet the API's production durability guarantees for 201/202 responses. Do not
expose it as a production API. The PostgreSQL adapter provides durable storage;
dispatch, recovery, artifact collection and analysis integration follow.

## Development

Use Go 1.26.9 (the tested toolchain, recorded in go.mod), or a reviewed newer Go
toolchain. PostgreSQL uses pgx v5.10.0; the Kubernetes API libraries are aligned
at v0.37.0; and the S3 adapter uses AWS SDK for Go v2 core v1.46.0,
config v1.33.3 and service/s3 v1.110.0.
go.mod and go.sum pin all dependencies. This Go repository does not need Python,
uv or Ruff.

Pull requests build the container and verify its platform, non-root identity,
entry point, source-revision label and offline registry validator. After a change
has merged to `main`, run **Build and optionally publish control-plane image**
manually with `publish=true`. The workflow pushes a unique run-scoped tag, pulls
the registry digest back, verifies the published bytes, and prints the immutable
GHCR reference for the environment repository. It does not publish mutable
release or `latest` tags.

Start with the [documentation map](docs/README.md) and
[contributor policy](CONTRIBUTING.md). The
[coding harness](docs/development/harness.md) defines pinned setup, command
tiers and complete local checks.
The [repository working agreement](AGENTS.md),
[engineering standards](docs/development/engineering-standards.md),
[SQL migration policy](docs/development/sql-migrations.md),
[architecture decisions](docs/decisions/README.md), [security policy](SECURITY.md)
and [code of conduct](CODE_OF_CONDUCT.md) define the rest of the contributor
harness. Cross-repository sequencing remains in the centralized
[perfeng roadmap](https://github.com/stanimirivanov/perfeng-environment/blob/main/docs/roadmap/milestones.md).
From this repository in PowerShell or a shell:

~~~sh
make bootstrap
make verify
make validate
~~~

The Makefile is the portable, authoritative command surface. `make verify` is
the fast network-independent loop after bootstrap; `make validate` adds race,
fuzz, vulnerability and license checks.

Lint findings, including vet, are informational: review them and decide which
changes improve the code. To format changes, run `make fmt`.
Race detection requires a supported C toolchain; CI runs it on Linux. Optional
bounded parser fuzzing:

~~~sh
go test -vet=off ./internal/httpapi -run=^$ -fuzz=FuzzParseJSON -fuzztime=10s
~~~

VS Code recommends the Go extension. Its language server provides formatting,
import organization and type checking; on-save lint uses the same informational
configuration as CI. Install the pinned linter before using it in the editor.
Formatting, test execution and reachable-vulnerability checks remain separate
CI gates. IntelliJ metadata, binaries, local state and secrets are ignored.

## Boundaries

| Package | Responsibility |
| --- | --- |
| internal/contract | Embedded, reviewed Run Management, Native Worker and data-contract snapshot |
| internal/run | Request/run types, lifecycle rules, repository contract |
| internal/memory | Atomic process-local test adapter |
| internal/nativeworker | Reviewed native assignments, capability matching, bounded lease selection and native artifact publication policy |
| internal/postgres | Durable transactions, migrations, native-worker leasing/upload reservations and immutable artifact storage |
| internal/httpapi | User and Native Worker HTTP parsing, isolated auth seams and response mapping |
| internal/authentication | Hashed static credentials with isolated user and native-worker audiences |
| internal/kubernetes | Deterministic Job dispatch, identity-checked observation, stop and Pod checks |
| internal/objectstore | Approved S3 location policy, artifact-byte verification and exact PUT presigning |
| internal/jsondocument | Shared bounded, duplicate-safe contract JSON handling |
| internal/normalizedresult | Untrusted normalized-result parsing and consistency validation |
| internal/analysisresult | Untrusted analysis-result parsing and consistency validation |
| internal/app | Executable configuration and dependency composition |
| internal/policy | Approved performance-policy parsing and report-verdict verification |
| internal/registry | Run admission, execution resolution, producer provenance and report trust from immutable startup entries |
| internal/rawresult | Untrusted raw-result manifest parsing and structural validation |
| internal/baseline | Baseline identity, evidence and forward-only approval lifecycle |
| internal/worker | Bounded claim scheduling, lease renewal and attempt cancellation |
| internal/reconcile | Lifecycle policy and lease-fenced persisted-execution reconciliation |

`httpapi.New(repository, authenticate, approve)` returns an `http.Handler`.
All dependencies are mandatory and must be concurrency-safe. HTTP tests show
composition with an exact-fixture approval stub.
`registry.ReportPolicyRegistry.ApproveRun` is the concrete immutable admission
adapter; production composition must populate it from reviewed, attested entries.
Approval also selects the server-owned execution target from producer provenance:
reviewed OCI Job execution is `KUBERNETES`, while reviewed source checkouts and
release archives are `NATIVE_WINDOWS`. The repository persists this decision
outside the public Run snapshot, and the Kubernetes reconciler claims only its
own target.

Authentication must verify the bearer credential and return a stable principal
with explicit Run and baseline operation permissions. Token rotation must
preserve the principal; do not use raw token bytes as identity. Missing or
invalid credentials return 401; lacking operation permission returns 403. Runs
and baseline versions belonging to other principals return 404. Resource
approval runs on every Run create request, including retries, so revoked access
is not bypassed by an idempotency key.

An approval adapter must resolve trusted catalogue, environment and policy
entries, verify hashes over exact published bytes, check suite/profile support,
candidate-image authorization, environment access and observe/inform policy
mode. Request-shape validation alone does not authorize execution. The handler
does not fetch caller URLs, accept commands, or treat synthetic fixture hashes
as approved resources. `ReportPolicyRegistry` performs exact allowlist matching
and candidate-image authorization after its startup entries have been attested.
Registry loading owns external I/O, byte verification and error classification;
safe sentinel errors map to 422, 403 or 503.

A service composition must wire verified identity/resource adapters and the
PostgreSQL adapter, TLS outside isolated development, bounded HTTP server timeouts,
admission/rate limits, observability and graceful shutdown before deployment.
This slice does not implement rate limiting or return REQUEST_IN_PROGRESS:
the in-memory adapter serializes acceptance, so concurrent identical calls
wait and receive the same original 201. Dependency unavailability returns 503
with Retry-After; internal diagnostics are never echoed to callers.

## State and persistence rules

The repository interface requires atomic create/binding persistence and
serialized cancellation/worker updates. The PostgreSQL adapter enforces these
in database transactions, not just process-local locks. Worker updates use an
expected revision and cannot overwrite a concurrent cancellation. No generic
HTTP endpoint exposes worker transitions.

Each mutation increments revision. Cancellation in CREATED or VALIDATING moves
directly to ABORTED; cancellation from PROVISIONING onward moves to CANCELLING
for external cleanup. Repeated cancellation in CANCELLING/ABORTED does not
mutate. Terminal states cannot resume. Tool exit 99 is an observed process
result and does not prevent COMPLETED when evidence is usable. Quality,
SLO and regression outcomes belong to perfeng-analysis; no measurement window
or result is fabricated here. This API is not the legacy run/v1 schema (which
cannot represent CANCELLING).

Go code uses the `run.State` and `run.FailureCode` constants for lifecycle
and failure decisions. Their JSON representation remains the contract's string
values; domain tests verify state coverage and failure-code compatibility.

In-memory run storage is intentionally unbounded and process-local; use only
bounded test/development workloads. A key can create a new run at expiry, but
the old run remains readable during this process's lifetime.

## PostgreSQL

See [storage and migration instructions](docs/postgresql.md) for schema ownership,
explicit migrations, the prototype migration boundary and isolated integration
tests. Ordinary `go test ./...` skips live PostgreSQL tests unless
`PERFENG_TEST_DATABASE_URL` is set. CI runs them against a pinned PostgreSQL
17.11 service, including a separate-process restart check.

## Reconciliation foundation

The PostgreSQL adapter also implements the privileged worker-only
`run.ReconciliationStore`: active-run discovery, renewable leases, delayed
release and lease/revision-checked updates. See
[reconciliation ownership](docs/reconciliation.md) for usage and limitations.
The `worker.Worker` engine claims only its available capacity, renews each active
lease and invokes an injected one-attempt reconciler. See the reconciliation
documentation for retry, cancellation and shutdown behavior. The Kubernetes
[dispatch boundary](docs/kubernetes-dispatch.md) adds one fixed
Job identity per Run and collision-checked create/adoption after uncertain API
responses. The same boundary observes the exact Job UID and requests foreground,
UID-preconditioned deletion. The bound-execution reconciler connects persisted
identities to those observation and stop boundaries, applying lifecycle changes
through the current lease and expected revision. `ProvisioningReconciler` resolves
an approved template through an injected interface, renews and rechecks the claim,
creates or adopts the deterministic Job, and binds its identity. Existing bindings
go directly to bound reconciliation, never back to creation.
`ValidationReconciler` resolves the pinned request without cluster I/O and uses
the dispatcher's policy to classify invalid or revoked plans before PROVISIONING.
`CollectionReconciler` waits for a verified raw-result manifest and its declared
objects, registers every immutable reference idempotently, and advances to
ANALYZING only after all writes succeed. `AnalysisReconciler` rediscovers that
evidence, invokes an idempotent normalization boundary, registers the normalized
result, and advances to REPORTING. Existing normalized evidence is recovered
without duplicate execution. `KubernetesAnalysisExecutor` resolves an approved
template, creates or adopts the Run's deterministic `-analysis` Job, maps its
identity-checked phase, and delegates successful output to a separate byte
attestor. `ReportingReconciler` rediscovers the normalized result, invokes an
idempotent report boundary only when no durable report exists, registers the
validated report reference, and advances to COMPLETED. A missing baseline or an
inconclusive quality verdict belongs in a successful report and does not make the
Run fail. `KubernetesReportExecutor` resolves an approved report template,
creates or adopts the Run's deterministic `-report` Job, maps its
identity-checked phase, and delegates successful output to a separate byte
attestor. The report resolver injects the exact digest-pinned producer and
policy-keyed baseline paths into a restricted reusable template. `Router`
advances CREATED and selects every active stage. Real catalogue and environment
publication verification, input materialization, unbound-cancellation recovery,
and production composition are still missing.
The `reconcile` policy defines that connection's state decisions independently
of I/O: pending Jobs wait, running Jobs enter RUNNING, terminal Jobs enter
COLLECTING, and unexpected disappearance/deletion is infrastructure failure.
Kubernetes failure never directly means TEST_FAILURE; artifact and process
evidence must be collected first. After dispatch begins, cancellation reaches
ABORTED only after the exact persisted execution is absent and no Pod owned by
its Job UID remains. Pre-dispatch cancellation requires no cluster cleanup.
API-server default normalization is covered by an isolated live integration test
described in the dispatch documentation.
The PostgreSQL adapter stores the accepted Job identity immutably so another
worker process can recover it under a current reconciliation lease.
Database leases fence lifecycle writes; deterministic Job identity makes replayed
creation safe without claiming exactly-once execution.

The [object-storage verification boundary](docs/artifact-storage.md) accepts only
the configured S3 bucket and each artifact's `runs/<run-id>/` namespace. It
returns bytes only after bounded reading and exact metadata, size and SHA-256
checks. An injected client still owns S3 authentication, endpoint configuration,
transport security and safe backend error classification.
The [runtime configuration boundary](docs/runtime-configuration.md) parses that
client policy separately from credential loading and adapter construction.
`objectstore.S3Getter` adapts the AWS SDK v2 `GetObject` operation and redacts
backend messages and request details. It preserves cancellation, distinguishes
missing keys, and classifies transient service/network failures as unavailable.
Its metadata adapter resolves a raw manifest only from the deterministic
`runs/<run-id>/raw-result.json` key and a configured v1 or v2 format. Discovery
does not replace the collector's independent byte verification.
The [raw-result parsing boundary](docs/raw-result-validation.md) validates the
producer envelope's exact structure, identities, timestamps and artifact claims.
Parsing does not approve provenance or attest remote bytes. A collector
composition accepts that approval and a trusted manifest publication reference
through separate injected boundaries, then verifies the manifest and every
declared object. Stable-key S3 publication discovery and immutable runtime
registry provenance approval both have concrete adapters.
The [normalized-result parsing boundary](docs/normalized-result-validation.md)
preserves unavailable statistics without fabrication, validates metric and
source consistency, and treats legacy thresholds as producer claims. A concrete
collector composition now verifies the trusted output reference and its bytes,
parses the envelope, binds its complete source set to the exact analysis input,
and requires provenance approval before registration. Deterministic S3
publication discovery supplies the immutable output reference. The collector
binds normalized claims to the verified persisted raw manifest, while the
immutable runtime registry authorizes the exact normalizer and accepted
execution context.
The same storage boundary discovers report output only from
`runs/<run-id>/analysis-result.json`; the report collector separately verifies
its bytes and binds it to the normalized candidate, policy, producer and
approved baseline evidence.
`app.NewReconciliationWorker` composes validation, provisioning, bound execution,
collection, analysis and reporting behind the lifecycle router and bounded
worker. Construction validates the complete dependency graph without calling
PostgreSQL, Kubernetes or object storage.
`app.Runtime` then supervises the HTTP and worker loops together, cancels the
peer on exit, and applies bounded graceful HTTP shutdown without choosing a
signal, authentication or deployment policy.

## Contract provenance

The [snapshot lock](internal/contract/snapshot/lock.json) pins perfeng-contracts
commit `143daa58774d8807f1745f8410ccab8b610170a0`, bundle 0.21.0,
Run Management API 0.4.0 and Native Worker API 2.2.0. The Run Management
OpenAPI document, transitions and selected request fixtures; the generated
performance-policy schema and browser example; and the Native Worker v2
OpenAPI, examples, rejection cases and HTTP exchanges are copied byte-for-byte
from that commit. The checksum ledger covers every local snapshot file, and
tests reject undeclared or absent files. There is no sibling-checkout or network
dependency at build, test or runtime. Imported contract content is Apache-2.0,
as in this repository.

The runtime contract boundary validates every
v2 operation's request and response shape, including complete leases and
recipes, upload grants, artifact registration and terminal outcomes. Conditional
cache/reuse and cancellation rules and ordered artifact slots are enforced.
The registry resolves a complete, reviewed v2 baseline recipe from an optional
`nativeBaseline` template and immutable Run context. The authenticated v2
presence, acquisition and lease-scoped HTTP operations use the v2 persistence
path. The v1 HTTP routes return 404. Acquisition is disabled by default and
requires explicit `PERFENG_NATIVE_WORKER_ACQUISITION_ENABLED=true` after the
Playwright worker is updated and a representative Windows run is verified.
Disabling acquisition does not prevent an already-issued lease from being
released or completed by its authenticated worker.

Review contract updates explicitly, refresh the snapshot and checksums, and
rerun tests. The current runtime validator implements only the schema features
reached by CreateRun, CreateBaseline, BaselineTransition and the pinned Native
Worker operation shapes, not arbitrary JSON Schema. The authenticated
`/native-worker/v2/presence`, `/native-worker/v2/leases`,
`/native-worker/v2/leases/{leaseId}/renew`,
`/native-worker/v2/leases/{leaseId}/release`,
`/native-worker/v2/leases/{leaseId}/upload-grants`,
`/native-worker/v2/leases/{leaseId}/artifacts`,
`/native-worker/v2/leases/{leaseId}/terminal`, and
`/native-worker/v2/attempts/inspect`, and
`/native-worker/v2/attempts/discover`
operations, typed worker-capability matching, reviewed native assignment
resolution and durable lease acquisition, renewal, release, non-secret upload
reservation and verified artifact acceptance are implemented.
Inspection returns a worker-scoped, read-only snapshot for one known Run and
attempt. It does not restore lease authority, authorize evidence deletion or
recover acquisition responses lost before the attempt ID became known.
Discovery accepts only `{"protocolVersion":2}` and derives scope from the
worker bearer principal. It returns stored availability and NONE or the single
current v2 slot, with one post-lock server clock. It neither enumerates history
nor restores a capability. Discovery remains available when new acquisitions
are paused. Ambiguous capacity fails closed rather than returning an arbitrary
attempt. See [discovery and manual recovery prerequisites](docs/native-worker-discovery.md).
Matching requires the worker to support the plan's browser, diagnostic mode,
execution-artifact transport and every artifact kind. Lease capabilities are
stored only as SHA-256 digests and returned only in `X-Perfeng-Lease`. The
snapshot defines upload authorization as a short-lived S3 SigV4 presigned
`PUT` URL with exact signed headers; the URL and headers are sensitive delivery
material rather than evidence. Upload reservation persists only a grant ID,
lease/attempt/slot fence, canonical object key, declared object facts and bounded
expiry. The AWS SDK adapter and native use case produce ephemeral delivery material
outside the reservation transaction, validate its actual signature expiry and
redact it from diagnostics. Runtime composition, authenticated upload-grant
delivery, artifact registration and terminal reporting are implemented.
Completion requires every required planned artifact and the named manifest to
have been registered last; it expires native authority and hands the Run to
`COLLECTING`. Classified failure instead persists the exact bounded worker
report and maps it to the established public Run failure taxonomy. Identical
terminal retries return the original resulting revision. Renewal, release,
upload reservation, final artifact acceptance and terminal reporting use the
native worker-before-Run lock order. Object verification occurs between short
transactions. The deployment must still
expose a worker-reachable HTTPS S3 endpoint before native uploads can pass live
acceptance.
Schema regular expressions are compiled once during initialization. Invalid
patterns, references or reachable unsupported constructs reject the embedded
snapshot before requests are handled.
Tests cover all lifecycle state pairs, strict request boundaries and observable
HTTP behavior. A broader cross-language conformance suite can follow.

## Baseline lifecycle

The [baseline domain](docs/baseline-lifecycle.md) represents one immutable
baseline version, its qualification evidence and its append-only review history.
PostgreSQL persists these records, serializes lifecycle decisions and resolves
only an explicitly pinned approved version whose trusted comparison dimensions
match. The authenticated HTTP boundary creates candidates, reads exact versions
and applies revision-checked qualification, approval and retirement decisions.
See the [baseline administration guide](docs/baseline-administration.md) for
permissions, request examples, conflict handling and uncertain-create recovery.

Relevant Go references: [HTTP server handlers](https://pkg.go.dev/net/http),
[JSON decoding behavior](https://pkg.go.dev/encoding/json), and
[race detection](https://go.dev/doc/articles/race_detector).
