# ADR-0003: Version the shared Native Worker lease authority

- Status: Proposed
- Date: 2026-10-03
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Retain one current lease row per Run across Native Worker API generations.
- Tag each row with its protocol version and preserve a foreign key to the
  matching generation's worker-presence table.
- Keep v2 acquisition disabled until the separate scheduling and authorization
  change is reviewed.

## Context

Migration 0013 separated v2 presence from v1 so old binaries and v1 scheduling
never decode a v2 report as v1. The current lease table still foreign-keys its
worker to v1 presence, so it cannot safely retain a v2 worker-only lease. Two
independent lease tables would lose the existing single-Run uniqueness boundary
and require cross-table coordination for capacity and recovery.

## Decision

Keep `native_execution_leases` as the shared, current Run authority. Migration
0014 adds `protocol_version` with a v1 default for existing rows and old v1
writers. Generated worker columns refer to the v1 or v2 presence table according
to that version. The shared primary key and expiry rules arbitrate Run ownership
across generations. Both acquisition paths lock existing presence rows for the
authenticated worker ID in v1-then-v2 order before the Run row; the shared
worker-capacity query is safe only after that serialization. The storage write
includes the protocol version, plan JSON and only a digest of the bearer
capability.

V1 mutation lookup accepts only v1 lease rows. The v2 acquisition transaction
uses the same authority, but the presence constraint does not authorize v2
workers. No v2 acquisition route is exposed, and v1 handlers cannot interpret
a v2 plan. V2 renewal and release use version-scoped lookup and the v2 presence
row; artifact and terminal mutations still require separate review and tests.

## Alternatives considered

### Separate v2 lease table

- Benefits: Easy generation-specific foreign keys and old-binary isolation.
- Costs and risks: Two independent primary keys cannot prevent both generations
  from leasing the same Run; old v1 binaries would not consult v2 ownership.
- Reason not selected: A single Run authority is a stronger concurrency fence.

### Drop the worker foreign key

- Benefits: Small migration and a single worker identifier column.
- Costs and risks: Direct or faulty writes could reference absent worker rows.
- Reason not selected: Generated generation-specific references retain database
  referential integrity without changing the existing worker identifier.

## Consequences

### Positive

- Existing rows remain v1, and v1 writers can omit the new column.
- The Run primary key, worker-capacity query and expiry recovery remain shared.
- A v2-only worker can be referenced without inserting a false v1 presence.

### Negative

- Generated stored columns require a table rewrite and an `ACCESS EXCLUSIVE`
  lock during migration. Schedule it in a maintenance window if the lease table
  is large; inspect its size and available disk/WAL capacity first.
- A pre-0014 binary does not understand a v2 lease row. Do not roll back to it
  after v2 issuance is enabled without first quiescing v2 acquisition and
  allowing active v2 leases to finish or expire.

### Neutral or follow-up

- The v2 presence gate remains false. Generation-aware selection, acquisition,
  renewal and release can be exercised on a disposable database; later work
  must add artifact and terminal mutations before issuance is enabled.
- Historical v1 terminal reports still reference v1 presence; v2 terminal
  reporting needs a separate compatible persistence change.

## Compatibility and migration

Migration 0014 preserves the old `worker_id` and plan fields, maps existing rows
to version 1, and uses a default so the running v1 binary can continue writing.
The current binary filters v1 mutation lookup by version. Deploy migration
before the new binary. Application rollback is safe while v2 issuance remains
disabled; older migrators still reject the newer checksum ledger as designed.
If migration fails, its transaction leaves the previous schema and ledger
unchanged. Repair forward with a new migration; do not edit 0014 after release.

## Security and operations

No extra runtime grant is required. Generated columns and foreign keys prevent
cross-generation worker references. Lease capabilities remain plaintext only
in the response and SHA-256 digests in PostgreSQL. Worker-before-Run locking
and the database clock remain the authority for future v2 acquisition.

## Validation

Test the empty migration chain and an upgrade with an existing v1 lease; verify
old-style v1 writes, both foreign keys, v2 plan and digest persistence, v1
mutation isolation, shared Run/worker occupancy and expiry replacement on
disposable PostgreSQL. Run the Go checks and race suite.
