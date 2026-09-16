# PostgreSQL migration policy

## TL;DR

- Migration files are immutable, append-only and applied in lexical order.
- Allocate the next four-digit number from fresh repository state and renumber
  a concurrent collision before merge.
- Preserve compatibility with the running and next application revision.
- Keep each migration focused, transactional and safe to retry through the
  checksum ledger and advisory lock.
- Verify both an empty database and the supported upgrade path with disposable
  PostgreSQL.

## Ownership and naming

The control plane owns schema `perfeng_control` and the migration runner in
`internal/postgres`. Migration files use:

```text
NNNN_short_description.sql
```

Immediately before creating a migration, refresh the branch as the workflow
allows and inspect `internal/postgres/migrations`. Allocate the lowest unused
four-digit number. A filename does not reserve a number outside its proposed
change.

If concurrent work uses the same number, refresh the branch, rename the later
migration to the next available number, and update tests and references. Never
combine unrelated SQL merely to retain a colliding number. Applied or published
migration names and bytes are immutable because the ledger verifies SHA-256.

## Evolution rules

- Prefer expand, migrate and contract across compatible releases.
- Add nullable columns or safe defaults before requiring new writes; backfill
  separately when volume or lock duration warrants it.
- Do not rename, remove or reinterpret a column in the same deployment that
  removes compatibility with the currently running binary.
- Do not use `IF EXISTS` or `IF NOT EXISTS` to conceal unexpected drift. The
  bootstrap ledger is the documented exception.
- Keep migrations transactional. Isolate an operation only when PostgreSQL
  cannot execute it safely in the migration transaction and document recovery.
- Use stable descriptive names for constraints and indexes.
- Columns are `NOT NULL` by default; every nullable value has one documented
  meaning.
- Use `timestamptz` for instants and make units and ownership explicit.
- Enforce stable invariants with database constraints in addition to application
  validation.
- PostgreSQL does not automatically index the referencing side of a foreign
  key; add indexes only for demonstrated access paths.

## Concurrency, locks and data changes

Before review, document applicable lock level and duration, table scans or
rewrites, index construction, WAL and disk effects, backfill size and restart
behavior, mixed-version compatibility, and forward recovery.

Large data changes MUST be resumable, idempotent, observable and separate from
long-held schema locks. Prefer set-based SQL. A code-driven backfill requires a
pinned implementation revision and explicit batching, retry and completion
verification.

Database transactions MUST NOT contain calls to Kubernetes, object storage or
other remote services. Preserve established row-lock ordering and lease fencing.

## Rollback and recovery

Down migrations are not the production rollback strategy. Roll back application
code while retaining compatible expanded schema, then repair forward. A change
that can lose or reinterpret data requires explicit authorization, backup and
restore prerequisites, and an ADR or migration plan.

Partial migration failure leaves the transaction unapplied. Checksum or ledger
disagreement is a drift error and MUST NOT be repaired by rewriting migration
history.

## Required verification

Every migration MUST, as applicable:

- apply the complete chain to an empty disposable PostgreSQL database;
- upgrade representative data from the latest supported schema;
- verify ordering, checksums and concurrent migrator serialization;
- run affected repository and integration tests, including race tests;
- test new constraints, transactions, scope isolation and row mapping;
- demonstrate old/new application compatibility for staged changes; and
- document operational or recovery steps that cannot be automated.

Never use a production database for migration tests. Follow
[docs/postgresql.md](../postgresql.md) for the isolated test environment.
