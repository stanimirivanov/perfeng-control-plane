# Performance control plane

Go implementation of the candidate run-management API and lifecycle from
performance-platform proposal sections 12-13. The control plane owns run
coordination, not load generation or statistical decisions.

## Current scope

- Authenticated Run and exact-version baseline administration handlers.
- Strict declarative request validation against the pinned contract.
- Atomic, principal-scoped idempotent creation with 24-hour retention.
- Original acceptance replay, current-state reads and asynchronous cancellation.
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
- Validated lifecycle entry and explicit routing for every active Run state.
- Retry-safe registration of verified raw artifacts before analysis.
- Restart-safe orchestration of normalization through an injected executor.
- Restart-safe report generation and durable completion through an injected executor.
- Duplicate-safe Kubernetes normalization Job creation and phase mapping.
- Duplicate-safe Kubernetes reporting Job creation and phase mapping.
- Bounded verification of immutable objects from approved S3 locations.
- AWS SDK for Go v2 adapter with safe S3 error classification.
- Strict parsing and structural validation of raw-result manifests.
- Composable verification of manifest provenance and every declared raw object.
- Strict parsing and internal-consistency validation of normalized-result envelopes.
- Composable verification of normalized output bytes, sources and provenance.
- Strict parsing and internal-consistency validation of analysis-result reports.
- Composable verification of report output bytes, candidate binding and approval.
- Exact report binding to run-pinned policy bytes, authorized producer and approved baselines.
- Strict performance-policy parsing and independent report-verdict verification.
- Versioned baseline candidates with revision-checked qualification, approval and retirement.
- Principal-scoped PostgreSQL baseline storage backed by completed, registered evidence.

This is a library foundation, **not a deployable service**. There is intentionally
no HTTP server executable, Docker image or Kubernetes deployment yet. The
administrative `cmd/migrate` command applies database migrations. Reconciliation
stages are not wired into a process. Trusted resource/template resolvers,
concrete publication/provenance adapters and concrete reporting execution are
still missing, so no
worker executes Jobs from accepted requests. Tests use synthetic contract
fixtures, not approved deployable resources.

The in-memory adapter loses runs and idempotency bindings on restart. It cannot
meet the API's production durability guarantees for 201/202 responses. Do not
expose it as a production API. The PostgreSQL adapter provides durable storage;
dispatch, recovery, artifact collection and analysis integration follow.

## Development

Use Go 1.26.6 (the tested toolchain, recorded in go.mod), or a reviewed newer Go
toolchain. PostgreSQL uses pgx v5.10.0; the Kubernetes API libraries are aligned
at v0.37.0; and the S3 adapter uses AWS SDK for Go v2 service/s3 v1.110.0.
go.mod and go.sum pin all dependencies. This Go repository does not need Python,
uv or Ruff.

Start with [contributing and Go code-quality standards](CONTRIBUTING.md) for
pinned tool installation, the review checklist and the complete local checks.
From this repository in PowerShell or a shell:

~~~sh
go test -vet=off ./...
golangci-lint run ./...
gofmt -l .
go test -vet=off -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
~~~

Lint findings, including vet, are informational: review them and decide which
changes improve the code. The separate gofmt check must print nothing.
To format changes: `golangci-lint fmt`.
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
| internal/contract | Embedded, reviewed API schema/transition snapshot |
| internal/run | Request/run types, lifecycle rules, repository contract |
| internal/memory | Atomic process-local test adapter |
| internal/postgres | Durable transactions, migrations and artifact-reference storage |
| internal/httpapi | HTTP parsing, auth/approval seams, status/response mapping |
| internal/kubernetes | Deterministic Job dispatch, identity-checked observation, stop and Pod checks |
| internal/objectstore | Approved S3 location policy and artifact-byte verification |
| internal/jsondocument | Shared bounded, duplicate-safe contract JSON handling |
| internal/normalizedresult | Untrusted normalized-result parsing and consistency validation |
| internal/analysisresult | Untrusted analysis-result parsing and consistency validation |
| internal/policy | Approved performance-policy parsing and report-verdict verification |
| internal/rawresult | Untrusted raw-result manifest parsing and structural validation |
| internal/baseline | Baseline identity, evidence and forward-only approval lifecycle |
| internal/worker | Bounded claim scheduling, lease renewal and attempt cancellation |
| internal/reconcile | Lifecycle policy and lease-fenced persisted-execution reconciliation |

`httpapi.New(repository, authenticate, approve)` returns an `http.Handler`.
All dependencies are mandatory and must be concurrency-safe. HTTP tests show
composition with an exact-fixture approval stub. That stub is not a production
registry implementation.

Authentication must verify the bearer credential and return a stable principal
with explicit Run and baseline operation permissions. Token rotation must
preserve the principal; do not use raw token bytes as identity. Missing or
invalid credentials return 401; lacking operation permission returns 403. Runs
and baseline versions belonging to other principals return 404. Resource
approval runs on every Run create request, including retries, so revoked access
is not bypassed by an idempotency key.

The approval adapter must resolve trusted catalogue, environment and policy
entries, verify hashes over exact published bytes, check suite/profile support,
candidate-image authorization, environment access and observe/inform policy
mode. Request-shape validation alone does not authorize execution. The handler
does not fetch caller URLs, accept commands, or treat synthetic fixture hashes
as approved resources. The eventual adapter owns resource-registry I/O and its
error classification; safe sentinel errors map to 422, 403 or 503.

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

Each mutation increments revision. Repeated cancellation in CANCELLING/ABORTED
does not mutate. Terminal states cannot resume. Tool exit 99 is an observed
process result and does not prevent COMPLETED when evidence is usable. Quality,
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
attestor. `Router` advances CREATED and selects every active stage. Real approved
resource/template resolvers, raw-manifest publication/provenance adapters,
report-output resolution and attestation, unbound-cancellation recovery, and
production composition are still missing.
The `reconcile` policy defines that connection's state decisions independently
of I/O: pending Jobs wait, running Jobs enter RUNNING, terminal Jobs enter
COLLECTING, and unexpected disappearance/deletion is infrastructure failure.
Kubernetes failure never directly means TEST_FAILURE; artifact and process
evidence must be collected first. Cancellation reaches ABORTED only after the
exact persisted execution is absent and no Pod owned by its Job UID remains.
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
`objectstore.S3Getter` adapts the AWS SDK v2 `GetObject` operation and redacts
backend messages and request details. It preserves cancellation, distinguishes
missing keys, and classifies transient service/network failures as unavailable.
The [raw-result parsing boundary](docs/raw-result-validation.md) validates the
producer envelope's exact structure, identities, timestamps and artifact claims.
Parsing does not approve provenance or attest remote bytes. A collector
composition accepts that approval and a trusted manifest publication reference
through separate injected boundaries, then verifies the manifest and every
declared object. Kubernetes publication discovery and a real provenance adapter
remain unwired.
The [normalized-result parsing boundary](docs/normalized-result-validation.md)
preserves unavailable statistics without fabrication, validates metric and
source consistency, and treats legacy thresholds as producer claims. A concrete
collector composition now verifies the trusted output reference and its bytes,
parses the envelope, binds its complete source set to the exact analysis input,
and requires provenance approval before registration. Kubernetes publication
discovery and the real approval policy remain deployment-specific adapters.

## Contract provenance

The [snapshot lock](internal/contract/snapshot/lock.json) pins perfeng-contracts
commit `305402970f286c5f84c8d2577e9f1ab3292c4b9c`, candidate bundle 0.8.0,
API 0.3.0. The OpenAPI document, transitions, run fixtures and baseline request
fixtures, plus the performance-policy schema and browser example, are copied
byte-for-byte from that commit; lock hashes cover these local bytes.
Tests verify checksums. There is no sibling-checkout or network dependency at
build/test/runtime. Imported contract content is Apache-2.0, as in this repository.

Review contract updates explicitly, regenerate the snapshot and checksums, and
rerun tests. The request validator implements only the schema features reached
by CreateRun, CreateBaseline and BaselineTransition, not arbitrary JSON Schema
or full response validation.
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
