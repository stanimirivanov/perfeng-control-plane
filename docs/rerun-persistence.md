# Immutable rerun intent persistence

## TL;DR

- M05-PR50 stages atomic child, service lineage and immutable acceptance storage.
- No HTTP route, application caller, public codec or execution activation is included.
- Actor/key replay is independent of legacy CreateRun and caller credential rotation.
- Current grants and exact baseline approval are fenced inside the write transaction.
- PR58/45 identity/authorization, PR59 execution and PR51 serving must qualify before use.

## Ownership and contract boundary

This implements the persistence foundation for [contracts PR49](https://github.com/stanimirivanov/perfeng-contracts/pull/116)
at merged commit `a1b9e554ab2b344d6481e6fec74d8a934821be21` (bundle 0.31.0,
Run API 0.8.0). `internal/runrerun` owns private intent/acceptance vocabulary;
`internal/postgres` owns transactional encoding and migration 0027. The embedded
public contract snapshot remains unchanged: private storage is not a public
RerunReceipt implementation or a completed rerun capability.

`AcceptRerun` is deliberately not called by serving/application composition.
Enabling a caller before PR59 consumes the stored selection would let ordinary
workers reselect resources and is unsupported. Authentication/delegation, complete
source-comparison admission, current catalogue/suite/environment/workload/policy
eligibility, artifact-byte/schema verification and access to every evidence owner
are prerequisites for the later inbound operation, not proved by this storage API.
Actor and caller inputs must come from independently verified identity, never a
browser payload or an unqualified BFF credential.

## Atomic acceptance and recovery

A transaction locks the actor issuer/ID/key namespace, then current source-service
`run:read` and `run:rerun` grant rows in permission order. Database time is read
after the locks. Revocation updates serialize against these shared locks; an old
read of a grant cannot authorize the write. Source service/creator/legacy owner
come from immutable reviewed `run_services`, not supplied service names.

Existing intent is replayed before current selection/approval checks. Source,
report UUID/digest, reason and actor kind must match; caller renewal preserves the
original caller. Exact expiry is a conflict, never a new child. Current rights
still apply, including read visibility on a different previously bound source
before returning a cross-source conflict. Legacy CreateRun semantics are unchanged.

New intent requires retained native report inputs and an exact registered source
analysis reference (`kind: normalized`, `analysis-result/v1`, JSON, 1 byte–1 MiB).
The retained policy/input digest is verified without network I/O. Each retained
baseline snapshot must still equal its APPROVED database record under shared row
locks, ordered by ID/version; no current/latest baseline is substituted. Registry
and object-store calls stay outside transactions. Publication-only sources without
native input capture are not inferred into this storage path; a separately reviewed
publication-backed adapter is required before admitting those sources.

Child Run, immutable inherited service binding, automatic binding audit and original
acceptance commit together. A failed receipt write rolls back all of them and
returns zero acceptance. The child's creator is the current actor; the acceptance
also preserves the source's original creator. `rerun:<sha256(actor identity)>` is
an internal native-owner namespace independent of the CI principal and caller;
reserve it from configured legacy identities when enabling service-mode admission.
It does not grant membership or allow the browser to select a native owner.

Receipts use a versioned private JSON envelope bounded to 64 KiB with a database
SHA-256 constraint. Full policy/approval inputs are read from immutable capture,
not duplicated into the receipt or projected as public JSON. Source/report/child
foreign keys and unique actor/key and child/receipt identities preserve association.
The SQL row is immutable: UPDATE, DELETE and TRUNCATE are refused. No retention
purger is introduced; keeping full receipts also keeps keys sealed. Any later
payload retirement must preserve tombstones through a reviewed migration.

`GetRerun` performs exact originating-actor/key/source recovery with a fresh read
grant, including after mutation permission is revoked. It does not dispatch work
or extend expiry. Unknown, foreign-actor, service-unbound and revoked selections
return the same storage not-found sentinel. Returned captured inputs remain private:
provider evidence authorization is required before public disclosure. Reopen and
process-exit tests recover the same original acceptance after an uncertain response.

## Migration, locks and rollback

Migration 0027 adds one empty table, constraints and guards only. It performs no
history scan/backfill, changes no existing rows and leaves old binaries compatible.
Normal schema/FK locks need the established bounded migration window; the new
primary/unique indexes cover actual lookup and child/receipt identities. The
source capture and service relations are prerequisites, not reconstructed data.

Apply through the explicit migration command using the trusted migration login.
Only the future qualified writer needs INSERT/SELECT on `rerun_intents` plus its
existing Run/service/baseline capabilities. Do not grant UPDATE/DELETE/TRUNCATE,
service-administrator privileges or schema ownership to runtime logins. Owners
and superusers remain trusted infrastructure, not an adversary these guards stop.
Write transactions request synchronous commit with existing 15-second operation
and 5-second lock bounds. Production fsync/backup/restore remain infrastructure
requirements. Row locks cover grants, sorted approvals and new child/service/FK
writes; no external calls occur while holding them. Work/disk grows per accepted
intent; no automatic audit retention policy is selected here.

Rollback application code while retaining migration, accepted children, service
lineage and receipts; repair forward. There is no destructive down migration,
activation flag or production deployment change. Future serving must wait for
identity, authorized comparison/byte reads and immutable execution fencing; it
must not expose this private object as the public contract body.

## Verification

Tests use a disposable loopback PostgreSQL database and pinned Go 1.26.11. They
cover empty/upgrade migration, no inferred history, concurrent duplicate acceptance,
distinct actor/key namespaces, changed intent/source conflicts, current revocation,
read-only recovery, exact baseline retirement behavior, report metadata limits,
SQL immutability, actual rollback after a receipt constraint failure, reopen and
separate process exit after commit without a response. Domain tests cover bounded
intent and the exact 24-hour replay deadline. Race checks supplement real database
serialization; they do not qualify future identity verification or dispatch behavior.
