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

`Router` advances CREATED and selects validation, provisioning, bound execution
raw-artifact collection or analysis from the claimed state. A registry-backed
resolver, object-storage collector, normalization executor, reporting stage and
production process wiring remain subsequent work.

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
| CANCELLING | absent | Confirm owned Pods are gone, then advance to ABORTED |
| Execution state | unexpectedly absent or deleting | INFRASTRUCTURE_FAILURE |

A failed Job advances to COLLECTING rather than TEST_FAILURE because Kubernetes
status cannot establish the runner exit code or whether usable diagnostic and
result artifacts exist. A terminal Job observed during PROVISIONING first enters
RUNNING to preserve an accepted state-machine path; the next observation can
advance it to COLLECTING.

The policy requires an existing durable execution identity. It does not decide
whether an unbound Job should be created or adopted.

## Validation and routing

`ValidationReconciler` accepts only VALIDATING claims. It resolves the principal's
pinned request through the same `JobResolver` used by provisioning, then calls
`Dispatcher.ValidateJob`. That validation applies deterministic identity and the
restricted execution policy without contacting Kubernetes or mutating the caller's
template.

A valid plan advances to PROVISIONING under the current lease and claimed revision.
`ErrValidation`, a nil template, or a revoked plan reported as `ErrForbidden`
advances to INVALID with a fixed safe `VALIDATION_FAILED` message. Resolver
unavailability, cancellation and lease loss remain operational errors and do not
change lifecycle state. A revision race requests immediate rediscovery.

Validation does not persist a mutable Job template. Provisioning resolves the
same immutable references again and rechecks ownership before dispatch. The real
resolver must enforce identity, version and checksum immutability so both reads
converge on the same plan.

`Router` has one explicit destination for every active state currently claimed
from storage:

| Run state | Destination |
| --- | --- |
| CREATED | Lease-fenced advance to VALIDATING |
| VALIDATING | Validation reconciler |
| PROVISIONING | Provisioning reconciler |
| WARMING_UP, RUNNING, CANCELLING | Bound-execution reconciler |
| COLLECTING | Raw-artifact collection reconciler |
| ANALYZING | Analysis reconciler |
| REPORTING | Deferred with bounded retry |

REPORTING is deliberately quiet rather than producing repeated worker errors
while its component is absent. Terminal and unknown states are rejected; the
storage contract must not claim terminal Runs.

CANCELLING currently routes to bound-execution reconciliation and therefore
requires a durable execution identity. Recovery for cancellation after an
ambiguous create but before identity binding remains a production-composition
gap; the router must not be wired into a production worker until that path has a
dedicated recovery stage.

## Raw-artifact collection

`CollectionReconciler` accepts only COLLECTING claims. Its injected
`RawArtifactCollector` returns the immutable raw-result manifest reference and
every object reference declared by that manifest. It must derive approved storage
locations from trusted configuration, verify the manifest and object bytes, and
attest storage ownership, checksums, sizes, media types, formats, Run identity
and producer provenance. Public run requests never supply arbitrary fetch
locations.

Storage visibility may lag execution completion. `ErrArtifactsNotReady` returns
a bounded quiet retry; other operational errors remain worker failures.
`ErrInvalidArtifacts` advances to TEST_FAILURE with a fixed `TOOL_ERROR` message
that does not persist parser, object-store or credential details. A successful
collector response is treated as a trusted-adapter boundary and must contain a
non-empty, unique set of valid raw references for the claimed Run. The manifest
is itself required to be an `application/json` raw artifact in `raw-result/v1`
format, with an identity and location distinct from its declared objects.

Declared object references and then the manifest reference are registered before
the Run advances to ANALYZING. Registration is immutable and idempotent, so
retrying after a partial write, timeout or uncertain commit safely repeats earlier
registrations. An artifact identity conflict is an operational error and never
overwrites existing evidence. A revision race after registration requests
rediscovery; cancellation wins the lifecycle write without invalidating already
verified immutable references.

This stage registers references only. The concrete S3-compatible collector must
still be implemented and must verify bytes before returning. It does not upload
objects, build raw manifests, dispatch analysis Jobs or interpret measurements.
Later analysis reconciliation can rediscover the registered manifest and source
objects through principal-scoped, artifact-ID-ordered storage listing; no
in-memory handoff from the collection attempt is required.

## Analysis reconciliation

`AnalysisReconciler` accepts only ANALYZING claims. It lists the principal-owned
artifact references, requires exactly one `raw-result/v1` JSON manifest and at
least one raw source, and rejects malformed, cross-Run or ambiguous persisted
evidence before invoking external work.

The injected `AnalysisExecutor` starts, adopts or observes idempotent
normalization using the immutable Run and an isolated input copy. It must use an
approved, digest-pinned analysis implementation, retrieve and verify the selected
object bytes, and attest a `normalized-result/v1` JSON object before returning its
immutable reference. The public Run request cannot supply executable commands or
artifact locations to this boundary.

`ErrAnalysisPending` produces a bounded quiet retry. `ErrAnalysisFailed` advances
to INFRASTRUCTURE_FAILURE with a fixed `ANALYSIS_ERROR` message; it never becomes
a performance regression verdict. Context cancellation, deadlines, lease loss,
storage unavailability and artifact conflicts take precedence over executor
classification and do not change lifecycle state.

The normalized reference is registered before advancing to REPORTING. If that
transition fails or has an uncertain outcome, the next attempt rediscovers the
existing normalized result, skips executor invocation and retries only the
lifecycle transition. Conflicting or multiple normalized results fail closed and
are never overwritten.

`KubernetesAnalysisExecutor` implements the process boundary with a deterministic
`<run-id>-analysis` Job. An injected resolver supplies the approved, digest-pinned
template. The Kubernetes adapter creates or adopts only an identically owned and
fingerprinted Job, maps pending/running/failed phases to the analysis contract,
and asks an injected output collector to attest bytes only after Job success.
Kubernetes completion by itself never creates an artifact reference.

Approved analysis image configuration, object downloads/uploads, concrete
template resolution and reporting decisions remain separate work.

## Provisioning reconciliation

`ProvisioningReconciler` accepts only PROVISIONING claims. It first loads the
durable execution binding under the current lease. If a binding exists, it invokes
the bound stage without resolving or creating a Job, even when that Job is absent
from Kubernetes. A persisted execution must never be silently replaced.

For an unbound Run, `JobResolver` receives the principal and an independent Run
snapshot. The resolver must authorize the pinned resources, verify their published
bytes and return an independently owned, reproducible Job template for that Run
ID and immutable request. It must not accept arbitrary manifests, commands or
credentials from API callers. This interface is not a registry implementation.

After resolution, the reconciler renews the lease and checks the latest lifecycle
state and revision. Observed cancellation or a revision change skips dispatch and
requests immediate rediscovery. Otherwise `EnsureJob` creates or adopts a matching
deterministic Job, and `BindExecution` persists its identity under the same lease.
The new-binding attempt does not advance lifecycle state; the next attempt uses
the bound stage's observed-state decisions.

The whole attempt has one context deadline, defaulting to 10 seconds within a
30-second lease. Configure the same lease TTL as the worker; the attempt deadline
must be positive and no greater than half that TTL. Dependencies must honor context
cancellation. This timeout bounds client work, not the outcome of an already-sent
Kubernetes request or database commit.

Retries always start with the binding lookup: an ambiguous committed binding
routes directly to the bound stage, while an unbound matching Job is adopted.
Dispatch, resolver and binding errors retain their identity; no cleanup deletes
an accepted Job merely because its binding failed. If cancellation races a
successful create, the returned identity is still bound when context and lease
ownership permit, without overwriting CANCELLING.

The pre-dispatch ownership check is not atomic with the Kubernetes request.
Unbound cancellation after an ambiguous create remains an explicit recovery gap;
this stage refuses CANCELLING claims and never creates a Job to cancel it. A full
router must address that gap before production use. Migrations and public API
behavior are unchanged by this stage.

## Bound execution reconciliation

`BoundExecutionReconciler` loads the immutable execution identity through the
current lease, obtains an identity-checked Job observation and applies decisions
with `AdvanceClaim` and the claimed Run revision. Pending work is released with
a validated rediscovery delay. A revision race is rediscovered immediately;
lease loss is an expected ownership outcome and is neither reported nor released
by the worker.

Cancellation stays inside one reconciliation attempt while the worker renews its
lease. The reconciler submits one UID-preconditioned foreground deletion request,
polls the exact Job until it is absent, then uses `kubernetes.StopVerifier` to
list Pods carrying the Run and manager labels. Every returned Pod must have a
controller reference to the persisted Job name and UID. Conflicting ownership is
an error, any remaining owned Pod keeps the Run in CANCELLING, and only an empty
verified list permits ABORTED.

This confirmation covers Kubernetes objects known to the API server. It cannot
prove a process on a partitioned node has stopped or prevent a stale in-flight
create from appearing later. Production composition still needs bounded dispatch
requests and a durable stop-intent/admission strategy; the platform does not claim
exactly-once execution.

The reconciler intentionally rejects a missing execution binding. Approved
template resolution, create/adopt and binding must run before this stage, with an
explicit recovery policy for cancellation that races an ambiguous create.

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

Likewise, the injected raw-artifact collector must verify durable evidence before
returning references to `CollectionReconciler`. This change adds no concrete
object-storage adapter, registry resolver, measurement-window discovery, retry
policy for load tests or analysis result.

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
