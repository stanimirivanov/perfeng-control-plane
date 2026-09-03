# Reconciliation work claims

The PostgreSQL reconciliation store and `worker.Worker` provide restart-safe,
bounded reconciliation attempts. Runs are discovered without an in-memory queue
or a caller-provided list of run IDs. This is coordination infrastructure, not a
Kubernetes lifecycle reconciler. No Jobs are created, stopped, retried or deleted
by the worker engine itself.

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
seconds. Empty/short batches under contention are normal, not proof that no work
exists.

## Worker engine

`worker.New` requires a reconciliation store, a one-attempt `Reconciler`, an
event reporter and validated configuration. The worker claims only its available
capacity and never runs more than the configured number of attempts. Each active
attempt renews its lease at an interval no greater than half the lease TTL.

The reconciler returns a delay before normal rediscovery. Delays use the store's
0-300 whole-second contract. A reconciler error or invalid delay is reported and
uses the configured failure delay; it does not stop the worker. Claim and release
failures are also reported. Events identify the run and operation but contain no
lease token. The reporter must return promptly and must handle diagnostic errors
without publishing secrets.

Lease loss is an expected concurrency outcome: the attempt is cancelled and the
worker neither reports nor releases the stale lease. Other renewal failures also
cancel the attempt and leave the lease to expire because ownership is uncertain.
When renewal first observes CANCELLING, the attempt receives
`ErrCancellationObserved` as its cancellation cause and releases immediately so
the run can be rediscovered without normal backoff.

Shutdown cancels active attempts, stops renewal and waits for their reconcilers
to return. It does not release their leases or claim that external effects have
stopped; durable leases become reclaimable after expiry. Reconciler implementations
must honor context cancellation promptly. A terminal transition expires its lease
inside the store, so a later release returning ErrLeaseLost is also normal.

The current injected reconciler is an interface seam. Resource resolution,
Kubernetes lifecycle mapping, artifact collection and production process wiring
remain subsequent work.

## Kubernetes lifecycle decisions

`reconcile.DecideBoundExecution` maps an identity-checked observation of a
durably persisted execution to one action. It deliberately performs no storage
or Kubernetes I/O, so transition policy can be tested independently from retry
and ownership mechanics.

| Run state | Job observation | Decision |
| --- | --- | --- |
| PROVISIONING | pending | Wait |
| PROVISIONING | running or terminal | Advance to RUNNING |
| WARMING_UP or RUNNING | pending or running | Wait |
| WARMING_UP or RUNNING | succeeded or failed | Advance to COLLECTING |
| CANCELLING | present | Request identity-checked stop |
| CANCELLING | absent | Advance to ABORTED |
| Execution state | unexpectedly absent or deleting | INFRASTRUCTURE_FAILURE |

A failed Job advances to COLLECTING rather than TEST_FAILURE because Kubernetes
status cannot establish the runner exit code or whether usable diagnostic and
result artifacts exist. A terminal Job observed during PROVISIONING first enters
RUNNING to preserve an accepted state-machine path; the next observation can
advance it to COLLECTING.

The policy requires an existing durable execution identity. It does not decide
whether an unbound Job should be created or adopted. The I/O reconciler that
resolves an approved template, persists dispatch identity, applies decisions and
polls foreground cancellation remains a subsequent slice.

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
worker can have an in-flight external request after ownership expires. Persisted
execution identity, deterministic names, ownership/specification checks and
duplicate-safe create/adoption make recovery possible, but they do not prevent
every ambiguous in-flight creation. These mechanisms do not prove exactly-once
Job execution or that an `ABORTED` Run has stopped executing.

The execution store closes the restart-recovery part of that gap. After
`EnsureJob` creates or adopts a Job, the worker binds `Dispatch.Execution()`
before relying on later observation. A restarted owner reads that immutable
identity using its new lease and observes the exact UID. If cancellation raced
an in-flight create, the old owner may still bind the returned identity while
its lease remains valid so a cancellation worker can recover and stop it.

The binding does not fence the Kubernetes request itself and does not authorize
reusing a deleted Job name. A conflict means the Run is already associated with
another UID or specification and must not be overwritten. A Kubernetes lifecycle
reconciler must handle that condition and ambiguous external/commit outcomes
explicitly.

Likewise, the future collector must verify durable evidence before leaving
COLLECTING. This change adds no registry resolver, measurement window, artifact
collection, retry policy for load tests, or analysis result.

## Storage, migrations and tests

Migration `0002_reconciliation_leases.sql` adds the lease table and migration
`0003_kubernetes_executions.sql` adds immutable Job identity; earlier migrations
are unchanged. Grant the trusted worker only the operations listed in
[PostgreSQL operations](postgresql.md). No credentials, PUBLIC grants or
destructive cleanup are applied automatically.

The existing PostgreSQL CI job runs the tests automatically. Locally, with a
disposable loopback PostgreSQL and PERFENG_TEST_DATABASE_URL configured:

~~~sh
go test -race -count=1 -timeout=3m -v ./internal/postgres
~~~

Tests cover bounded worker concurrency, renewal, cancellation propagation,
retry delays, lease-loss and shutdown behavior. Storage tests cover multiple
pools/workers, locked-row skipping, lease renewal/expiry,
same-worker-ID fencing, retry delay and cancellation priority, stale-revision
rejection, immutable execution binding, separate-process restart, and migration
from a populated v1 database without losing runs, acceptance bindings or
artifact references.

Reference: [PostgreSQL SELECT locking clauses](https://www.postgresql.org/docs/17/sql-select.html).
