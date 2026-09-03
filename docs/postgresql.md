# PostgreSQL storage

The control plane owns the `perfeng_control` schema. Infrastructure owns the
PostgreSQL service, backups, database and login provisioning; analysis and
runners do not write these tables directly.

| Table | Purpose |
| --- | --- |
| schema_migrations | Applied forward migrations and SHA-256 checksums |
| runs | Principal-owned current run snapshots |
| create_bindings | Original acceptance snapshot and 24-hour key expiration |
| artifacts | Immutable identities for raw/normalized object references |
| reconciliation_leases | Worker ownership tokens, lease expiry and retry availability |
| kubernetes_executions | Immutable Job UID and specification identity per Run |

Snapshots are JSONB using the accepted API field names, with relational identity,
ownership, uniqueness and foreign-key constraints. This keeps optional API
fields and timestamps intact while allowing state indexes. Lifecycle validation
belongs to the Go domain; direct table writes are not an alternate public API.

## Transactions and durability

Create serializes on a transaction-scoped advisory lock derived from the
unambiguously encoded create operation, principal and key. The full database
primary key determines identity; a hash collision only causes extra waiting.
The run and acceptance binding commit together. The database clock is read after
acquiring the lock; expiry is not calculated from a stale transaction-start time.

Replays return the original CREATED snapshot and expiration, even after the run
has progressed. Expired keys may bind to new runs without deleting old evidence.
Cancellation and worker transitions lock the current run row; worker writes also
check the expected revision. No-op cancellation does not update revision.

Write transactions set synchronous_commit=on. Production still requires server
fsync, reliable persistent storage, backup/restore testing and any required
synchronous-replication policy. This is not proof of HA or database-crash recovery.
Integration tests prove persistence across independent service processes.

The pool is bounded to 16 connections per repository, with 15-second operations
and 5-second lock waits. Transient connection/locking errors map to UNAVAILABLE;
context cancellation and deadlines retain their standard error identity.
A commit error may mean the operation committed: retry create with the same
key/body, never automatically with a new key. Unexpected PostgreSQL errors expose
only a validated five-character SQLSTATE, never server messages, row contents or
connection credentials.

## Explicit migrations

Only run migrations against a database intentionally assigned to this service.
Opening a repository does not modify its schema. Set `PERFENG_DATABASE_URL`
through your secret-management mechanism, then run:

~~~sh
go run ./cmd/migrate
~~~

The command prints connection/migration progress, returns nonzero on failure,
and never prints the DSN. It is safe to rerun after checking an uncertain outcome.
Do not put a real connection string in the repository, terminal transcripts or
issue text. Use TLS with certificate verification outside isolated development.

Embedded migrations are applied in filename order under an advisory lock.
Schema changes and the version ledger are transactional. Unknown versions,
checksum drift and ledger gaps are rejected. Do not edit an already applied
migration; add a new numbered file. There is deliberately no automatic down,
drop, truncate or prototype-import operation.

Use a migration login with schema-creation/ownership rights, separate from the
runtime login. Once migrations have completed, the runtime role needs:

~~~sql
GRANT USAGE ON SCHEMA perfeng_control TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.runs TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.create_bindings TO perfeng_runtime;
GRANT SELECT, INSERT ON perfeng_control.artifacts TO perfeng_runtime;
~~~

Only the trusted reconciliation worker role additionally needs SELECT, INSERT
and UPDATE on `perfeng_control.reconciliation_leases`, plus SELECT and INSERT on
`perfeng_control.kubernetes_executions`. It also needs the Run table privileges
above. Do not grant the worker interfaces to tenant/API callers. Migration 0002
adds leases; migration 0003 adds immutable execution identity without altering
earlier migrations or existing data.
See [worker-claim semantics](reconciliation.md).

Provision that role/login through infrastructure; the migration does not create
credentials or grant permissions to PUBLIC. Do not use the schema owner as the
runtime login. Artifact UPDATE/DELETE and schema DDL are intentionally excluded.
The role needs no extensions, sequences, superuser or database-creation rights.

## Prototype migration boundary

The prototype's `platform/db/migrations` and storage repositories informed
this implementation, but are not applied here:

- `metadata.test_runs` supplies the intent to retain run identity/state;
  UUIDs and pending/running/failed statuses cannot be silently converted to the
  new API's perf-* IDs and richer lifecycle.
- `metadata.data_artifacts` supplies the intent to store object references,
  sizes and checksums, not object bytes in PostgreSQL.
- Prototype environment snapshots, events and result statistics remain outside
  this slice. No values, measurement windows or analysis verdicts are invented.

Existing `metadata.*` tables remain untouched. Any historical data conversion
needs an explicit ID/state mapping and separately reviewed migration. There is
no history extraction here: this is a new Go adapter and incompatible new schema,
not a relocation of the prototype SQL.

## Artifact references

`run.Artifact` follows the fields of artifact/v1 from perfeng-contracts commit
`220140137a2e70367f3d6aa3bde8aede4d49c8b7`. `RegisterArtifact`, `GetArtifact`
and `ListArtifacts` are worker-only interfaces, not new HTTP routes. IDs are
stored as canonical lowercase UUIDs; sizes fit PostgreSQL/Go signed 64-bit
integers. Storage policy also rejects credentials, query strings and fragments
in artifact URLs.

An identical retry is a no-op. Rebinding an artifact ID to different content,
location or run returns ErrArtifactConflict. Registration checks run ownership
and persists references only: the `RawArtifactCollector` must verify object bytes,
checksums and approved storage before returning them to reconciliation. This does
not guarantee object retention or prevent someone overwriting objects in S3. No
objects are fetched or uploaded.
Adding evidence is a separate entity write, not a Run snapshot mutation.

`ListArtifacts` returns every reference for a principal-owned Run ordered by
artifact ID. Stable ordering and durable storage let an analysis worker recover
after restart without retaining manifest or object IDs in memory. An owned Run
with no references returns an empty list; a missing or cross-principal Run returns
`ErrNotFound`, matching the repository's visibility boundary. Listing metadata
does not fetch or attest the referenced object bytes.

## Kubernetes execution identity

The first accepted Kubernetes Job identity is stored separately from the public
Run snapshot. It contains only Run ID, namespace, deterministic Job name, Job
UID and canonical specification hash. Mutable Job status is not persisted here.

`BindExecution` and `GetExecution` require a current reconciliation lease and
lock the Run row used by renewal, cancellation and lifecycle transitions. An
identical bind is a no-op. A different identity returns `ErrExecutionConflict`
without replacing the original row. This remains true across connections,
concurrent calls and worker process restarts.

A worker may bind an execution after the Run becomes `CANCELLING`. This is
intentional: cancellation may race an in-flight Kubernetes create, and losing
the returned UID would make safe cleanup impossible. Terminal Runs and stale,
expired or forged leases cannot read or bind execution identity. An uncertain
commit must be retried only with the same identity.

## Isolated integration tests

Start a disposable PostgreSQL 17 instance with a local-only connection and a
test role allowed to CREATE DATABASE. Set `PERFENG_TEST_DATABASE_URL` to its
administrative database, then:

~~~sh
go test -race -count=1 -timeout=3m -v ./internal/postgres
~~~

Tests reject non-loopback endpoints. They create random
`perfeng_test_<16 hex characters>` databases and drop only those databases on
cleanup; they never migrate or clear the database named in your DSN. Stop/remove
your disposable PostgreSQL instance when finished. Tests close all connections
before cleanup, and report a generated database name if cleanup fails.

The GitHub integration job provisions the environment's exact PostgreSQL 17.11
image digest and supplies the DSN automatically. Its trust authentication is
restricted to a disposable CI service; never copy that setting into production.

Coverage includes empty-database migration, repeat migration, checksum rejection,
transaction rollback, original acceptance replay, principal isolation, immutable
artifacts, key expiry, duplicate acceptance across pools, cancellation/completion
races, bounded lock waits, immutable execution-binding races and a fresh OS
process recovering persisted records.

References: [PostgreSQL locking](https://www.postgresql.org/docs/17/explicit-locking.html)
and [pgx database/sql adapter](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib).
