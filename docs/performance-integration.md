# Performance integration contract boundary

## TL;DR

- The provider pins integration API 1.0.0 from contracts bundle 0.24.0.
- `internal/integrationcontract` validates wire messages; it grants no authority.
- Reviewed registry bindings resolve requests; durable admission and HTTP remain delivery work.

## Pinned interchange

The schema, OpenAPI document and completed/cancelled/rejected exchanges under
`internal/contract/snapshot` come from perfeng-contracts commit
`041273947703542f7fe22eab838c176df5517974`. The component-specific lock records
their checksums without replacing existing catalog or native contract pins.
The published HTTP namespace is `/integration/v1/requests`; this change does
not register those routes or change any existing API generation.

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
race, vulnerability and license checks. This boundary changes no database or
live Kubernetes behavior; those gates belong to the later affected slices.
