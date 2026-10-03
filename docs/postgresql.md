# PostgreSQL storage

## TL;DR

- The control plane exclusively owns the `perfeng_control` schema; infrastructure
  owns the PostgreSQL service, backups and credentials.
- Transactions enforce principal scope, idempotent creation, lifecycle revision
  checks, reconciliation and native-worker leases, and immutable
  Kubernetes/artifact identities.
- Migrations are explicit, embedded, checksummed, transactional and serialized
  by an advisory lock; opening the repository never migrates automatically.
- Storage changes require disposable PostgreSQL integration, concurrency and
  restart tests.
- Follow the separate SQL migration policy before adding a migration.

The control plane owns the `perfeng_control` schema. Infrastructure owns the
PostgreSQL service, backups, database and login provisioning; analysis and
runners do not write these tables directly.

| Table | Purpose |
| --- | --- |
| schema_migrations | Applied forward migrations and SHA-256 checksums |
| runs | Principal-owned current Run snapshots and server-selected execution targets |
| create_bindings | Original acceptance snapshot and 24-hour key expiration |
| artifacts | Immutable identities for raw/normalized object references |
| reconciliation_leases | Worker ownership tokens, lease expiry and retry availability |
| kubernetes_executions | Immutable Job UID and specification identity per Run |
| baselines | Principal-owned baseline versions and lifecycle snapshots |
| native_worker_presence | Last authenticated capability and qualification report per native worker |
| native_worker_presence_v2 | Latest authenticated v2 capability and qualification report, isolated from v1 scheduling |
| native_execution_leases | Current fenced, protocol-versioned native attempt, reviewed plan and hashed lease capability per Run |
| native_upload_grants | Durable non-secret declaration and exact object identity per native attempt and slot |
| native_artifacts | Server-issued identity and verified location for an accepted native upload grant |
| native_terminal_reports | First accepted native attempt outcome and retry-stable resulting Run revision |

Snapshots are JSONB using the accepted API field names, with relational identity,
ownership, uniqueness and foreign-key constraints. This keeps optional API
fields and timestamps intact while allowing state indexes. Lifecycle validation
belongs to the Go domain; direct table writes are not an alternate public API.

## Transactions and durability

Create serializes on a transaction-scoped advisory lock derived from the
unambiguously encoded create operation, principal and key. The full database
primary key determines identity; a hash collision only causes extra waiting.
The Run, its server-selected execution target and acceptance binding commit
together. The database clock is read after acquiring the lock; expiry is not
calculated from a stale transaction-start time. A live idempotency replay must
match both the original request and execution target.

Replays return the original CREATED snapshot and expiration, even after the run
has progressed. Expired keys may bind to new runs without deleting old evidence.
Cancellation and worker transitions lock the current run row; worker writes also
check the expected revision. CREATED and VALIDATING cancellation commits ABORTED
without external cleanup. Later active states commit CANCELLING. No-op
cancellation does not update revision.

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
GRANT SELECT, INSERT, UPDATE ON perfeng_control.baselines TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.native_worker_presence TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.native_worker_presence_v2 TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.native_execution_leases TO perfeng_runtime;
GRANT SELECT, INSERT, UPDATE ON perfeng_control.native_upload_grants TO perfeng_runtime;
GRANT SELECT, INSERT ON perfeng_control.native_artifacts TO perfeng_runtime;
GRANT SELECT, INSERT ON perfeng_control.native_terminal_reports TO perfeng_runtime;
~~~

Only the trusted reconciliation worker role additionally needs SELECT, INSERT
and UPDATE on `perfeng_control.reconciliation_leases`, plus SELECT and INSERT on
`perfeng_control.kubernetes_executions`. It also needs the Run table privileges
above. Do not grant the worker interfaces to tenant/API callers. Migration 0002
adds leases, migration 0003 adds immutable execution identity, migration 0004
adds baseline storage, and migration 0005 adds last-seen native-worker presence
without altering earlier migrations or existing data. Migration 0006 persists
the server-selected execution target, and migration 0007 adds current native
execution ownership without backfilling or changing existing Runs. Migration
0008 adds nullable native-release markers and a bounded consistency constraint;
it does not rewrite existing lease values or Run snapshots. Migration 0009 adds
the initially empty native-upload-grant table without scanning, rewriting or
backfilling existing tables. Migration 0010 adds accepted native artifact
identities, and migration 0011 adds the first accepted native terminal outcome;
both are additive empty-table migrations. Migration 0012 widens the native
artifact format check to accept canonical Native Worker API values such as
`playwright-measurements/v2` and `raw-result/v2`.
Migration 0013 adds the empty v2 presence table without changing existing v1
rows or constraints. The old binary ignores it; the new binary's v1 lease path
continues to use only the v1 table. The v2 table constrains
`authorized_to_acquire` to false until a later reviewed migration enables v2
leasing.
Migration 0014 versions the shared native lease table. Existing and old-style
v1 writes use protocol version 1; generation-specific generated foreign keys
bind each lease to its matching presence table. The shared Run primary key and
worker occupancy query apply across versions. V2 issuance remains disabled by
the v2 presence authorization constraint. Adding generated stored columns
rewrites the lease table under an `ACCESS EXCLUSIVE` lock: inspect its size,
disk/WAL headroom and maintenance window before production migration. See
[ADR-0003](decisions/0003-version-native-lease-authority.md) for compatibility
and rollback constraints.
See [worker-claim semantics](reconciliation.md).

Provision that role/login through infrastructure; the migration does not create
credentials or grant permissions to PUBLIC. Do not use the schema owner as the
runtime login. Artifact UPDATE/DELETE and schema DDL are intentionally excluded.
The role needs no extensions, sequences, superuser or database-creation rights.

## Baselines

Migration 0004 adds one row for each principal, baseline ID and semantic version.
Relational source-Run, artifact, revision and state columns are constrained to
match the JSON snapshot. The source Run must belong to the principal, and the
artifact foreign key binds the exact registered artifact to that Run.

`CreateBaseline` reads the database clock, validates a new candidate, locks its
completed source Run for consistent verification, and compares the supplied
normalized reference with the immutable artifact registry. It returns
`ErrNotFound` for missing or cross-principal evidence, `ErrValidation` for an
unfinished Run or mismatched reference, and `ErrConflict` when the requested
baseline version already exists.

`TransitionBaseline` locks the baseline row before reading the database clock
and applying its expected revision. Qualification evidence, state, revision and
append-only lifecycle history commit in one update. Concurrent decisions from
the same observed revision cannot both commit. `GetBaseline` uses the same
principal visibility boundary as Run reads.

`ResolveApprovedBaseline` selects only a caller-supplied ID and semantic version
that is currently `APPROVED`, then verifies the complete trusted test, workload,
environment and dataset compatibility dimensions against the decoded snapshot.
Missing, cross-principal, unapproved, retired and incompatible records all return
an empty result. The storage interface does not list versions or promote the
latest successful Run, so it cannot introduce baseline drift. The
[HTTP administration boundary](baseline-administration.md) applies separate
create, read and transition permissions without changing these storage rules.

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
`82baa78c9a08851431688f647e69a09d66da42f1`. `RegisterArtifact` and
`GetArtifact` remain worker-only interfaces. `ListArtifacts` also backs the
contracted read-only HTTP collection for a principal-visible Run. IDs are stored
as canonical lowercase UUIDs; sizes fit PostgreSQL/Go signed 64-bit integers.
Storage policy also rejects credentials, query strings and fragments in
artifact URLs.

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
does not fetch or attest the referenced object bytes. The HTTP representation
wraps this slice in an `artifacts` object property and preserves an empty JSON
array rather than encoding unavailable evidence as `null`.

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

## Native execution leases

The ownership and capability model is proposed in
[ADR-0001](decisions/0001-use-expiring-native-lease-capabilities.md).
Native scheduling first reads one fresh, AVAILABLE, qualified and authorized
presence observation, then resolves compatible reviewed plans outside database
transactions. Final acquisition locks the worker's existing v1 and v2 presence
rows in that order before the selected Run row and rechecks the exact presence
timestamp, freshness, capabilities, worker
capacity, Run target/state/revision and competing Run ownership using the
database clock. Registry resolution and all other external work remain outside
the transaction.

Native Worker capacity is one active lease per worker across both API generations.
The common presence-row lock order serializes a worker reporting both protocols
even when acquisitions target different Runs. Concurrent workers
can consider the same Run, but the shared Run-row lock permits only one active
lease. Expired rows are replaced with new lease and attempt UUIDs and new bearer
authority, allowing crash recovery without preserving process-local state.
Acquisition deliberately leaves the Run in CREATED: coordination metadata does
not claim that preparation or execution has started, and an expired attempt can
be planned again. Authenticated renewal owns those lifecycle transitions. The
v2 transaction is implemented but the database authorization gate remains
closed until v2 renewal, release and terminal paths are ready; no v2 HTTP
acquisition route is exposed yet.

The worker receives a random lease capability once. PostgreSQL stores only its
SHA-256 digest; JSON and diagnostic formatting exclude plaintext capability
bytes. A commit failure has an uncertain outcome and does not return the
capability. The possibly committed row becomes recoverable only after expiry.
The v1 authenticated acquisition HTTP operation composes its transaction with
reviewed plan resolution. It validates the complete returned lease before
placing the plaintext capability only in `X-Perfeng-Lease`; no-work responses
do not distinguish which eligibility condition was absent.

Renewal pre-reads only the lease location, then locks the authenticated worker,
Run and current lease in that order before rechecking worker, lease, attempt,
capability digest, expiry and expected Run revision. It uses the database clock
to extend expiry and atomically advances monotonic worker phases. `PREPARING`
crosses the internal validation and provisioning states into `WARMING_UP`,
`RUNNING` enters `RUNNING`, and `UPLOADING` or `FINALIZING` enters or remains in
`COLLECTING`. A `CANCELLING` Run takes precedence over phase advancement and
returns a bounded stop deadline without changing its revision. Terminal,
expired and mismatched authority share the lease-gone result.
The Native Worker HTTP boundary supplies the authenticated worker identity and
header-only capability, validates the pinned request and maps stale revision to
`409` and lost authority to `410`.

Explicit release uses the same lock order and complete authority check. The
first release expires the lease at database time and records a bounded reason
and optional message. A retry for the same attempt and reason is a no-op even
after cancellation changed the Run revision; a different reason conflicts.
`CANCELLATION_REQUESTED` is accepted only for `CANCELLING` and advances that Run
to `ABORTED` in the same transaction. Other reasons expire coordination only
and cannot make partial artifacts or terminal evidence authoritative. A
released CREATED Run is immediately eligible for a new attempt, which replaces
all lease authority and clears the old release marker.

Migration 0008 takes the ordinary short `ALTER TABLE` lock needed to add three
nullable columns and validate one constraint over the current lease table. An
older binary can continue using unreleased rows. If it encounters a row already
released by the new binary, the constraint prevents it from extending or
replacing that authority without clearing the marker; this fails closed during
a mixed-version rollout. Roll back the binary while retaining the expanded
schema and repair forward.

## Native upload grants

The upload-grant reservation follows the accepted direct-publication decision
in perfeng-contracts ADR-0003. It authenticates the worker, lease capability,
attempt and expected Run revision under the same worker-before-Run-before-lease
lock order as renewal and release. It then matches the declared media type and
size to the persisted reviewed artifact plan and selects the canonical key
`runs/{runId}/attempts/{attemptId}/{slot}`. The grant expires no later than both
the server's bounded grant lifetime and current lease authority.

PostgreSQL stores the random grant ID, lease and attempt fence, Run, slot,
canonical object key, declared size and SHA-256, assigned maximum size, media
type and timing. It deliberately has no URL, query, signed-header or plaintext
capability column. Presigned request material is a delivery credential and must
be generated outside the database transaction. An identical retry for a lease
and slot retains the grant ID and refreshes its bounded timing; a changed size,
checksum or media type conflicts instead of rewriting the declaration.

The lease table keeps only current authority and replaces its row when an
expired or released CREATED attempt is recovered. Upload reservations therefore
retain lease and attempt UUIDs as historical fences without foreign-keying them
to that replaceable row. Their Run foreign key preserves durable ownership, and
unique lease/slot and object-key constraints prevent two identities from
claiming the same attempt output.

Migration 0009 creates only a new empty table, its primary and unique indexes,
constraints and foreign key. It takes catalog and new-relation locks but no lock
that scans or rewrites existing application rows; there is no backfill, data WAL
or existing-table growth. Older binaries ignore the table, while the new binary
requires the migration before reserving a grant. Roll back application code
while retaining the table, allow any separately delivered presigned requests to
expire, and repair forward; do not remove issued declarations or rewrite the
migration.

## Native artifact registration

Registration first selects an immutable artifact candidate under the current
worker, lease, attempt, capability and Run-revision fence. The candidate joins
the durable upload declaration with the artifact kind and format from the
persisted reviewed plan. That transaction releases all locks before object
storage is read.

After the object metadata, byte count and SHA-256 have been verified, the final
transaction reacquires the worker-before-Run-before-lease lock order, locks the
upload grant and repeats every authority and declaration check. Losing the
lease or changing the Run revision during object verification therefore makes
the result ineligible for acceptance. An identical retry returns the first
server-issued artifact ID and registration time.

The `native_artifacts` table stores only the generated artifact ID, referenced
grant, canonical S3 URI, reviewed kind and format, and database registration
time. The grant remains the single durable source for Run, lease, attempt,
slot, object key, size, checksum and media type. Plaintext lease capabilities,
presigned URLs and signed request headers are never persisted.

Migration 0010 creates one empty table, its primary and unique indexes, and a
foreign-key constraint. It takes catalog and new-relation locks plus the short
lock PostgreSQL needs to add the foreign key against `native_upload_grants`; it
does not scan,
rewrite or backfill existing application rows. Older binaries ignore the new
table. Roll back application code while retaining accepted identities and
repair forward rather than editing the migration.

Migration 0012 replaces only the `native_artifacts.format` check. It accepts
the pinned contract's format shape without changing existing rows or meaning.
Adding the replacement check validates existing rows and takes an `ALTER TABLE`
lock on `native_artifacts`; schedule it before sustained artifact traffic. The
migration is transactional, and older binaries can continue using existing
formats. Roll back application code without reversing the expanded check.

## Native terminal reporting

Terminal reporting uses the established worker-before-Run-before-lease lock
order and compares the full authenticated worker, lease, attempt, capability
digest and expected Run revision. A first report, its lifecycle effect, the
resulting Run revision and lease expiry commit in one transaction. Once that
report exists, an identical request returns `ALREADY_ACCEPTED` even though the
lease is expired; a different report for the attempt conflicts and cannot
replace the durable outcome.

`COMPLETED` names one registered manifest from the same Run and attempt. The
transaction parses the persisted reviewed artifact plan, requires every
required slot, checks each registered kind, format and media type, and requires
the selected manifest to have been registered no earlier than every other
accepted attempt artifact. The Run enters or remains in `COLLECTING`; terminal
reporting does not skip manifest parsing, provenance approval, normalization or
reporting.

`FAILED` preserves the exact contract-valid code, message and retryability hint
in `native_terminal_reports`. The public Run snapshot continues to use its
smaller established failure taxonomy and a fixed safe message: `TOOL_FAILED`
becomes `TEST_FAILURE`, while preparation, capture, upload and lost host
qualification become `INFRASTRUCTURE_FAILURE`. Cancellation remains release
semantics and is not accepted as ordinary terminal reporting.

Migration 0011 creates one empty table with primary, unique, foreign-key and
shape constraints. It does not scan, rewrite or backfill existing rows. The
foreign keys take PostgreSQL's ordinary short relation locks while the new
table is created. Older binaries ignore the table; rolling application code
back retains accepted outcomes for forward recovery, and the migration bytes
must not be edited.

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
races, bounded lock waits, immutable execution-binding races, serialized baseline
approval, native lease capacity/acquisition races and expiry recovery, and a
fresh OS process recovering persisted records. Native artifact coverage also
includes exact retry, stale declaration rejection, authority loss between
verification and commit, and upgrade from the upload-grant schema. Native
terminal coverage includes complete and ordered evidence, exact retry,
conflicting concurrent outcomes, authority and revision rejection, classified
failure mapping, lease expiry and upgrade from the artifact-registration schema.

References: [PostgreSQL locking](https://www.postgresql.org/docs/17/explicit-locking.html)
and [pgx database/sql adapter](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib).
