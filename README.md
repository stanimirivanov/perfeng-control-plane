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

This is a library foundation, **not a deployable service**. There is intentionally
no server executable, Docker image or Kubernetes deployment yet. No jobs execute,
and cancellation remains CANCELLING until a future worker confirms execution has
stopped. Tests use synthetic contract fixtures, not approved deployable resources.

The in-memory adapter loses runs and idempotency bindings on restart. It cannot
meet the API's production durability guarantees for 201/202 responses. Do not
expose it as a production API. PostgreSQL persistence is the next implementation
slice; dispatch, recovery, artifact collection and analysis integration follow.

## Development

Use Go 1.26.4 (the tested toolchain, recorded in go.mod), or a reviewed newer Go
toolchain. There are no third-party Go dependencies and no go.sum is needed.
This Go repository does not need Python, uv or Ruff.

From this repository in PowerShell or a shell:

~~~sh
go test ./...
go vet ./...
go test -race ./...
gofmt -l .
~~~

The last command must print nothing. To format changes: `gofmt -w internal`.
Race detection requires a supported C toolchain; CI runs it on Linux. Optional
bounded parser fuzzing:

~~~sh
go test ./internal/httpapi -run=^$ -fuzz=FuzzParseJSON -fuzztime=10s
~~~

VS Code recommends the Go extension; install its Go tools when prompted.
Editor settings enable gofmt/import organization, and Go's language server
provides type checking and static analysis. CI requires formatting, vet, tests
and race detection. IntelliJ metadata, binaries, local state and secrets are
ignored.

## Boundaries

| Package | Responsibility |
| --- | --- |
| internal/contract | Embedded, reviewed API schema/transition snapshot |
| internal/run | Request/run types, lifecycle rules, repository contract |
| internal/memory | Atomic process-local test adapter |
| internal/httpapi | HTTP parsing, auth/approval seams, status/response mapping |

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

A service composition must add verified identity/resource adapters, durable
storage, TLS outside isolated development, bounded HTTP server timeouts,
admission/rate limits, observability and graceful shutdown before deployment.
This slice does not implement rate limiting or return REQUEST_IN_PROGRESS:
the in-memory adapter serializes acceptance, so concurrent identical calls
wait and receive the same original 201. Dependency unavailability returns 503
with Retry-After; internal diagnostics are never echoed to callers.

## State and persistence rules

The repository interface requires atomic create/binding persistence and
serialized cancellation/worker updates. A future SQL adapter must enforce these
in database transactions, not just process-local locks. Worker updates use an
expected revision and cannot overwrite a concurrent cancellation. No generic
HTTP endpoint exposes worker transitions.

Each mutation increments revision. Repeated cancellation in CANCELLING/ABORTED
does not mutate. Terminal states cannot resume. Tool exit 99 is an observed
process result and does not prevent COMPLETED when evidence is usable. Quality,
SLO and regression outcomes belong to perfeng-analysis; no measurement window
or result is fabricated here. This API is not the legacy run/v1 schema (which
cannot represent CANCELLING).

In-memory run storage is intentionally unbounded and process-local; use only
bounded test/development workloads. A key can create a new run at expiry, but
the old run remains readable during this process's lifetime.

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
Tests cover all lifecycle state pairs, strict request boundaries and observable
HTTP behavior. A broader cross-language conformance suite can follow.

Relevant Go references: [HTTP server handlers](https://pkg.go.dev/net/http),
[JSON decoding behavior](https://pkg.go.dev/encoding/json), and
[race detection](https://go.dev/doc/articles/race_detector).
