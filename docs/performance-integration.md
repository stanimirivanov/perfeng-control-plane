# Performance integration contract boundary

## TL;DR

- The provider pins integration API 1.0.0 from contracts bundle 0.24.0.
- `internal/integrationcontract` validates wire messages; it grants no authority.
- Request admission, durable correlation and HTTP routes remain delivery work.

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
3. **PR6c: durable request admission and reads** — atomically bind authenticated
   request identity to a native Run, persist accepted/rejected dispositions,
   preserve replay beyond the native create-key lifetime and expose scoped
   create/read operations. Keep external calls outside transactions.
4. **PR6d: request lifecycle and recovery** — project monotonic native revisions,
   handle scoped cancellation and deadline recovery, and retain correlation or
   tombstones so expiration cannot silently create duplicate runs.

PR6 is complete only after those behaviors, their PostgreSQL concurrency gates
and authenticated HTTP flows ship. No interface stubs are introduced here.

## Verification

Run `go test -vet=off -count=1 ./internal/integrationcontract ./internal/contract ./internal/architecture`
for focused conformance, snapshot integrity and dependency boundaries.
`make fmt` and `make validate` provide the repository acceptance gates, including
race, vulnerability and license checks. This boundary changes no database or
live Kubernetes behavior; those gates belong to the later affected slices.
