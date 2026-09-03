# Performance control plane

Go implementation of the candidate run-management API and lifecycle from
performance-platform proposal sections 12-13. The control plane owns run
coordination, not load generation or statistical decisions.

## Current scope

- Authenticated create, get and cancel HTTP handlers.
- Strict declarative request validation against the pinned contract.
- Atomic, principal-scoped idempotent creation with 24-hour retention.
- Original acceptance replay, current-state reads and asynchronous cancellation.
- Revision-checked worker transitions, terminal-state and failure-code rules.
- In-memory repository for bounded tests/development, with concurrency tests.
- PostgreSQL repository, transactional migrations and immutable artifact references.
- Durable worker claims, lease renewal/recovery and fenced lifecycle updates.
- Duplicate-safe Kubernetes Job creation and adoption boundary.

This is a library foundation, **not a deployable service**. There is intentionally
no HTTP server executable, Docker image or Kubernetes deployment yet. The
administrative `cmd/migrate` command applies database migrations. No worker
executes Jobs, and cancellation remains CANCELLING until a future worker confirms
execution has stopped. Tests use synthetic contract fixtures, not approved
deployable resources.

The in-memory adapter loses runs and idempotency bindings on restart. It cannot
meet the API's production durability guarantees for 201/202 responses. Do not
expose it as a production API. The PostgreSQL adapter provides durable storage;
dispatch, recovery, artifact collection and analysis integration follow.

## Development

Use Go 1.26.6 (the tested toolchain, recorded in go.mod), or a reviewed newer Go
toolchain. PostgreSQL uses pgx v5.10.0; the Kubernetes API libraries are aligned
at v0.37.0. go.mod and go.sum pin all dependencies. This Go repository does not
need Python, uv or Ruff.

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
| internal/kubernetes | Deterministic Kubernetes Job creation and safe adoption |

`httpapi.New(repository, authenticate, approve)` returns an `http.Handler`.
All dependencies are mandatory and must be concurrency-safe. HTTP tests show
composition with an exact-fixture approval stub. That stub is not a production
registry implementation.

Authentication must verify the bearer credential and return a stable principal
and create/read/cancel permissions. Token rotation must preserve the principal;
do not use raw token bytes as identity. Missing/invalid credentials return 401;
lacking operation permission returns 403. Runs belonging to other principals
return 404. Resource approval runs on every create request, including retries,
so revoked access is not bypassed by an idempotency key.

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
The Kubernetes [dispatch boundary](docs/kubernetes-dispatch.md) adds one fixed
Job identity per Run and collision-checked create/adoption after uncertain API
responses. It does not add the worker loop, Job observation or cancellation.
Database leases fence lifecycle writes; deterministic Job identity makes replayed
creation safe without claiming exactly-once execution.

## Contract provenance

The [snapshot lock](internal/contract/snapshot/lock.json) pins perfeng-contracts
commit `220140137a2e70367f3d6aa3bde8aede4d49c8b7`, candidate bundle 0.5.0,
API 0.1.0. The OpenAPI document, transitions and create fixture were mechanically
compacted as JSON with LF final newlines; lock hashes cover these local bytes.
Tests verify checksums. There is no sibling-checkout or network dependency at
build/test/runtime. Imported contract content is Apache-2.0, as in this repository.

Review contract updates explicitly, regenerate the snapshot and checksums, and
rerun tests. The request validator implements only the object/string/reference
subset used by CreateRun, not arbitrary JSON Schema or full response validation.
Schema regular expressions are compiled once during initialization. Invalid
patterns, references or reachable unsupported constructs reject the embedded
snapshot before requests are handled.
Tests cover all lifecycle state pairs, strict request boundaries and observable
HTTP behavior. A broader cross-language conformance suite can follow.

Relevant Go references: [HTTP server handlers](https://pkg.go.dev/net/http),
[JSON decoding behavior](https://pkg.go.dev/encoding/json), and
[race detection](https://go.dev/doc/articles/race_detector).
