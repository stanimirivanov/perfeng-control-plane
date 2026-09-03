# Reconciliation work claims

The PostgreSQL reconciliation store is a prerequisite for a restart-safe worker.
It discovers active runs without an in-memory queue or a caller-provided list of
run IDs. This is coordination infrastructure, not a Kubernetes dispatcher.
No Jobs are created, stopped, retried or deleted by this code.

## Worker-only interface

`run.ReconciliationStore` is separate from the public run API:

| Method | Behavior |
| --- | --- |
| ClaimRuns | Claim up to 1-100 eligible active runs across principals |
| RenewClaim | Extend an owned lease and return the current Run snapshot |
| AdvanceClaim | Check lease ownership and expected revision before a lifecycle mutation |
| ReleaseClaim | End ownership and optionally delay the next poll |

Only trusted control-plane workers may receive this interface/database role.
Cross-principal discovery is deliberate: it is not an authenticated tenant
listing endpoint. Claim tokens must not be exposed in HTTP responses or logs.

Worker IDs are 1-128 ASCII letters/digits/dot/underscore/colon/hyphen, starting
with a letter/digit. Lease TTL is 5-300 whole seconds; retry delay is 0-300 whole
seconds. Use small batches appropriate to worker capacity and renew well before
expiry. Empty/short batches under contention are normal, not proof that no work
exists. A worker loop and its heartbeat/shutdown policy are subsequent work.

## Ownership and concurrency

Claims use short PostgreSQL transactions and row locks with SKIP LOCKED, sharing
the Run row lock used by cancellation and lifecycle writes. Eligibility is
rechecked in a new statement after row locking: an older JOIN snapshot must not
allow a worker to replace a lease another transaction just committed.

The PostgreSQL implementation keeps that sequence visible in three layers:

1. `ClaimRuns` owns the timeout, transaction, batch loop and single commit.
2. `lockClaimCandidates` selects and locks Run rows, decodes their snapshots,
   and closes query rows before any further statement uses the transaction.
3. `tryAcquireClaim` reads database time, rechecks one candidate's current
   lease, and writes its replacement when eligible.

All acquired leases therefore commit or roll back together. Helper return does
not release a Run lock; transaction completion does. Do not perform registry,
Kubernetes, object-storage, or other external I/O between selection and commit.

Lease deadlines use the database clock. The caller's ExpiresAt is only a hint.
Every new claim gets a cryptographically random token, including when the same
worker ID reclaims the same run. An expired token cannot renew, release or mutate
work, before or after reassignment. Errors return ErrLeaseLost without changing
the new owner's lease. Owner, principal, run ID and token must all match.

AdvanceClaim also checks the expected Run revision. Cancellation does not wait
for lease expiry: it updates the run under the shared row lock. A worker holding
an older revision cannot overwrite CANCELLING; RenewClaim returns the latest
snapshot so the worker can react to it. Cancellation bypasses release/backoff
delays but does not steal a still-live lease. Terminal runs are never discovered,
and a terminal AdvanceClaim expires the lease in the same transaction.

Claim, renewal and release change only coordination metadata, not Run revisions
or the original idempotent acceptance response. Existing records need no queue
backfill; they become discoverable after migration 0002. Leases survive service
restart and become reclaimable on expiry. There is no automatic lifecycle
transition merely because a worker disappeared.

## Failure handling and external side effects

Workers using this store must use AdvanceClaim, not the older unfenced Advance
method. The latter remains available to trusted existing domain/storage callers;
it is not a lease-aware scheduler write.

On ErrLeaseLost, stop writing as that owner. On ErrRevision, read the current
snapshot through renewal before deciding what to do. A timeout/UNAVAILABLE at
commit can leave the outcome uncertain; do not assume the lease or transition
was rolled back. An unacknowledged new lease eventually expires.

Input validation returns ErrValidation before storage access. After a Run is
locked, missing Run/lease rows, owner or token mismatch, expiry, and terminal
state are intentionally reported as the same ErrLeaseLost value. This prevents
the privileged interface from exposing whether another principal or worker owns
the Run. AdvanceClaim verifies ownership before revision/transition validation.

**A database lease does not fence Kubernetes requests.** A paused/partitioned
worker can have an in-flight external request after ownership expires. The future
dispatcher still needs persisted execution identity, deterministic Job names,
ownership/spec checks, duplicate-safe create/adoption, and cancellation handling
for ambiguous in-flight creation. These claims alone do not prove exactly-once
Job execution or that an ABORTED run has stopped executing.

Likewise, the future collector must verify durable evidence before leaving
COLLECTING. This change adds no registry resolver, measurement window, artifact
collection, retry policy for load tests, or analysis result.

## Storage, migrations and tests

Migration `0002_reconciliation_leases.sql` adds a separate table; migration
0001 is unchanged. Add SELECT/INSERT/UPDATE on this table to the trusted worker
role after applying migrations. No new credentials, PUBLIC grants, or destructive
cleanup are applied automatically. See [PostgreSQL operations](postgresql.md).

The existing PostgreSQL CI job runs the tests automatically. Locally, with a
disposable loopback PostgreSQL and PERFENG_TEST_DATABASE_URL configured:

~~~sh
go test -race -count=1 -timeout=3m -v ./internal/postgres
~~~

Tests cover multiple pools/workers, locked-row skipping, lease renewal/expiry,
same-worker-ID fencing, retry delay and cancellation priority, stale-revision
rejection, separate-process restart, and migration from a populated v1 database
without losing runs, acceptance bindings or artifact references.

Reference: [PostgreSQL SELECT locking clauses](https://www.postgresql.org/docs/17/sql-select.html).
