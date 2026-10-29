# Performance integration contract boundary

## TL;DR

- The provider pins integration API 1.0.0 from contracts bundle 0.24.0.
- `internal/integrationcontract` validates wire messages; it grants no authority.
- Reviewed bindings enable durable admission, reads, cancellation and recovery.

## Pinned interchange

The schema, OpenAPI document and completed/cancelled/rejected exchanges under
`internal/contract/snapshot` come from perfeng-contracts commit
`041273947703542f7fe22eab838c176df5517974`. The component-specific lock records
their checksums without replacing existing catalog or native contract pins.
The published HTTP namespace is `/integration/v1/requests`; native API generations
remain unchanged. Cancellation and deadline recovery use the same durable mapping.

`integrationcontract.DecodeIntegrationMessage` accepts at most 64 KiB of
unique-key UTF-8 JSON. It rejects unknown fields, unsupported message variants,
invalid formats and inconsistent per-message time/identity relationships.
`EncodeIntegrationMessage` applies the same checks before returning any bytes.
Both return the safe `contract.ErrIntegration` on invalid input. The validator
loads only embedded resources; incoming references cannot trigger network or
filesystem access. Schema compilation occurs once; calls can run concurrently.

The core transport types retain explicit nullable baseline, evidence references,
source algorithm/digest, image digest and all immutable context pins. Revisions
use `json.Number` to preserve integer-valued exponent syntax and the published
safe-integer bound. Timestamps retain their original offsets and up to six
fractional digits. Events cannot predate request creation; acceptance must
precede the deadline. Late lifecycle evidence and cancellation remain valid.

## Authority and recovery boundaries

Successful decoding establishes message conformance only. A claimed principal,
issuer, catalog revision, baseline or candidate binding is not authorization.
The runtime must authenticate the caller, verify provider response identity,
resolve reviewed registry bindings and check current approval independently.
For example, a null baseline is valid interchange but need not be admissible.

The adapter has no clock, persistence or side effects. It cannot decide replay,
conflicting event IDs, immutable context equality across messages, terminal
monotonicity, cancellation precedence or retention. Do not substitute schema
validation for these durable workflow checks.

## Delivery sequence for M09-PR6

The original correlated-request task is split to keep contracts, admission and
durability independently reviewable. Later slices may split further when their
implementation exceeds the agreed review size.

1. **PR6a: pinned contract boundary** — these models, decoder/encoder, offline
   schema validation and portable exchange tests. No runtime exposure.
2. **PR6b: reviewed admission bindings** — resolve approved service, capability,
   deployment, source/image and evidence pins into an existing native Run input.
3. **PR6c: durable request admission and reads** — storage slice PR6c1 atomically binds
   request identity to a native Run, persists accepted/rejected dispositions,
   and preserves replay beyond the native create-key lifetime. The dependent
   PR6c2 transport/composition slice exposes authenticated create/read operations.
   Keep external calls outside transactions. The storage contract and rollout
   are specified in [ADR-0013](decisions/0013-retain-integration-admission-atomically.md).
4. **PR6d: request lifecycle and recovery** — project monotonic native revisions,
   handle scoped cancellation and deadline recovery, and retain correlation or
   tombstones so expiration cannot silently create duplicate runs.

PR6 is complete only after those behaviors, their PostgreSQL concurrency gates
and authenticated HTTP flows ship. No interface stubs are introduced here.

## Reviewed admission bindings

An entry may opt in with an `integration` member alongside its existing
`catalogOwnerRoute`. The top-level `performanceCatalog` configuration supplies
the provider and authenticated-principal issuer namespaces. Existing bundles
without these members retain their behavior and cannot resolve integration work.

```json
"integration": {
  "requestIssuer": "https://argus.example/identity",
  "service": {"issuer":"https://services.example", "id":"search", "revision":"1", "sha256":"<64 lowercase hex>"},
  "capability": {"issuer":"https://services.example", "id":"search-query", "revision":"1", "sha256":"<64 lowercase hex>"},
  "deployment": {"issuer":"https://deployments.example", "id":"search-staging", "revision":"7", "sha256":"<64 lowercase hex>"},
  "candidate": {"gitSha":"<40 lowercase hex>", "image":"ghcr.io/example/search@sha256:<64 lowercase hex>"},
  "evidence": [{"issuer":"https://evidence.example", "id":"reviewed-deployment-proof", "revision":"1", "sha256":"<64 lowercase hex>"}]
}
```

Replace placeholders with actual reviewed pins. The bundle reviewer must verify
that the deployment belongs to the service/capability, targets the entry's exact
environment, and deploys this source/image pair; evidence must substantiate that
association. A digest or claimed Git revision alone establishes none of it.
The candidate image must also appear in the existing `candidateImages` allowlist.
An explicit empty evidence array is permitted only when the reviewer has another
trusted basis for the association; omitted/null evidence is rejected. Duplicate
evidence identities are rejected. No URL is fetched or inferred from a request.

`ReportPolicyRegistry.ResolveIntegration` takes a wire-validated context and an
independently authenticated principal. It requires exact namespace and pin
equality, including evidence order, and maps the immutable workload/profile,
environment and policy references using the same projection as the catalog.
SHA-256 Git revisions cannot be mapped to the existing SHA-1 native API.
Several matching entries fail closed, rather than choosing by map order.

The optional reader has the same signature as `GetBaseline`, including an
explicit principal. Policies requiring one baseline re-read it and require
current APPROVED compatible state plus the exact catalog revision/content hash.
Policies requiring several baselines are unsupported; SLO-only policies require
a null baseline. Missing or stale approval is forbidden; backend failures remain
unavailable and their details are not exposed. This read is not an approval lock:
PR6c must define the atomic acceptance boundary and revalidate as needed.

This method creates no work, checks no admission clock and grants no HTTP access.
Bindings are defensively copied at startup and are safe for concurrent reads.
Deploy the compatible binary before adding configuration. For binary rollback,
remove each `integration` member first; old binaries reject unknown fields.

## Verification

Run `go test -vet=off -count=1 ./internal/integrationcontract ./internal/contract ./internal/architecture`
for focused conformance, snapshot integrity and dependency boundaries.
`make fmt` and `make validate` provide the repository acceptance gates, including
race, vulnerability and license checks. Run `make db-validate` against a disposable
PostgreSQL instance for durable admission, replay, scoped HTTP reads, and rejection
coverage. These routes do not change live Kubernetes behavior.

## Authenticated HTTP

The runtime enables POST `/integration/v1/requests` and GET
`/integration/v1/requests/{requestId}?issuer=...` only when reviewed integration
bindings exist. `runs:create` and `runs:read` respectively authorize these operations;
`catalog:read` grants neither. Supply one bearer header and POST `application/json`
with the bounded shared request envelope. No native Idempotency-Key is required:
the durable context/event identities govern replay. Principal issuer and ID must
match configured authentication before any admission, even for expired requests.

Enabled runtime uses the reviewed native resolver and atomic native repository;
legacy-only custom implementations fail configuration rather than dropping the
binding. New accepted work retains native analysis associations; historical unbound
rows remain absent on replay. See [runtime composition](performance-analysis-publication.md#runtime-composition).

POST returns 202 with the original accepted disposition or 422 with the retained
rejection. Retrying a timed-out POST must preserve identity/context; reconcile
uncertain acceptance by GET. Conflicting reuse is 409. Temporary backend/auth
unavailability returns 503 with Retry-After; malformed/unsupported wire versions
are 400, credential failure 401, missing permissions/principal mismatch 403 and
unsupported content type 415. Errors contain only bounded code/message fields.

GET requires exactly one issuer parameter and an escaped opaque request ID,
with no body or extra parameters. Unknown and cross-principal lookups return 404.
Accepted requests return 200 lifecycle snapshots at the current native revision;
rejected requests return 422 with their unchanged disposition. Snapshot IDs remain
stable for an unchanged revision. CREATED through WARMING_UP project to ACCEPTED,
RUNNING through REPORTING to RUNNING, CANCELLING to CANCELLING, COMPLETED to
COMPLETED, INVALID/INFRASTRUCTURE_FAILURE/TEST_FAILURE to FAILED, and ABORTED to
CANCELLED. Completion does not assert a passing performance verdict.

Apply migrations 0019 and 0020 and the existing table-level runtime grants before
deploying current integration readers or enabling bindings. Unsupported
durable storage fails composition rather than falling back to memory. Existing
configurations without bindings retain their behavior. Disable bindings before
binary rollback and keep retained ledgers. No requests or event digests are deleted.

## Cancellation and recovery

POST `/integration/v1/requests/{requestId}/cancellation?issuer=...` requires
`runs:cancel`, the shared cancel-requested envelope, matching principal namespace
and the full original immutable context. Path/query identity must match the body.
Unknown/cross-principal requests return 404; retained rejection, changed context
or conflicting event reuse return 409. A missing mapping never creates a Run.
Reconcile uncertain admission through scoped GET and retry original identities.

Cancellation and the delivery digest commit atomically. Unstarted CREATED or
VALIDATING work becomes CANCELLED without dispatch. Active work returns a 202
CANCELLING snapshot until native termination is confirmed; a terminal snapshot
returns 200, preserving a completion/failure that won concurrently. Repeated
cancellation does not increment revisions. Original admission replay is unchanged.

With reviewed integration bindings, runtime recovery scans at startup and each
worker poll independently of reconciliation capacity and HTTP traffic. It locks
up to 100 overdue active mapped Runs per transaction, skips locked rows, checks
database time, and requests native cancellation. Future, rejected, CANCELLING and
terminal work is unchanged. Transient recovery failures are classified through
the existing worker reporter and retried on subsequent polls. Restart resumes
from durable state; deadline expiry alone never proves external work stopped.

Correlation, original dispositions and delivery digests are retained indefinitely,
including terminal and rejected requests. Retention replaces retirement tombstones
in this implementation; there is no retirement operation and no 410 response is
currently needed. Do not delete ledger rows to retry or enable another execution.
See [ADR-0015](decisions/0015-recover-integration-cancellation.md).
