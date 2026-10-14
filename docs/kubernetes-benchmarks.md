# Contained Kubernetes benchmark lifecycle

## TL;DR

- OCI kube-burner dispatch requires an explicitly reviewed startup entry and
  fresh admission of the selected `local-kind-integration/1.0.0` environment.
- PostgreSQL reserves the logical target before namespace creation. The hold is
  not a worker lease and never expires; verified cleanup permits later reuse.
- Namespace UID, exact request bytes and deterministic Job identity survive
  partial failures and restarts; foreign or changed resources are not repaired.
- This is M04 dispatch infrastructure, not a promoted deployable bundle or
  production-calibrated performance comparison.
- Cancellation seals dispatch, stops the exact executor and cleans only owned
  workloads/authority. Unknown termination retains `CANCELLING` and the hold.
- Cleanup retains an empty namespace/zero-Pod quota, drains bounded requests
  and persists its outcome for recovery; raw collection precedes normal cleanup.

## Source and responsibility boundaries

The control plane owns authorization, live admission, durable reservations and
scoped dispatch. `perfeng-k8s` owns the executable adapter, its container and
publication protocol; `perfeng-environment` owns reviewed environment selection.
No request field permits a client to supply a command, namespace, cluster
endpoint, service account, image override or cleanup authority.

The implementation consumes these reviewed sources:

| Source | Revision | Meaning |
|:--|:--|:--|
| perfeng-contracts | `3b86afa18b468d831c7c2c176618180db18b2764` | 0.22.0 admission/evidence semantics |
| perfeng-environment | `9dbd7f84872ed03a1caa32c60ce0ebaf791bbd01` | Reviewed local integration profile and fingerprint algorithm |
| perfeng-k8s | `a0e2b670bea91af182aaff78c925f4a3c6d1d861` | Contained workload, Pod source and runner/publisher protocol |

The environment profile is embedded unchanged, with its SHA-256 checked by
selection validation. Workload and Pod-source hashes are fixed in
`internal/benchmark`. This does not update the existing HTTP contract snapshot,
promote an execution image, or alter ordinary k6 and Native Worker admission.
Previously synthetic OCI kube-burner entries without benchmark configuration
now fail startup validation deliberately; there is no generic dispatch bypass.

## Trusted registry configuration

A benchmark entry uses the existing principal, catalogue, policy, profile,
environment and candidate authorization. In addition it requires:

- `testId: pod-startup-k8s`, contracts version `0.22.0`, and kube-burner `2.8.5`;
- one reviewed candidate: the exact packaged pause-image digest;
- the exact `pod-startup-contained/1.0.0` workload hash;
- `rawProducer.artifact.kind: oci-image` selecting a digest-addressed
  `ghcr.io/stanimirivanov/perfeng-kube-burner` image;
- a reusable execution template matching `kubernetes.BenchmarkJobTemplate`;
- a `benchmark` object with `clusterId`, `clusterUid`, `environmentFile`,
  `templateSha256` and `bucket`.

`clusterId` is `perfeng-local`. `clusterUid` is the actual `kube-system` namespace
UID from the reviewed qualification. `environmentFile` is the exact descriptor
emitted by environment qualification: its bytes, digest and fingerprint must
match the entry. It is not a short-lived permission token. The operator reviews
the corresponding qualification receipt, including the Docker node-image
checks which an in-cluster controller cannot independently perform. Live API
inventory must reproduce the selected node UIDs, runtimes and capacity.

The template builder defines the narrow supported recipe, not an extension
point for arbitrary workload code. Secret names and the storage endpoint are
reviewed template values. Only HTTPS or the exact local SeaweedFS HTTP endpoint
is accepted. Runner and publisher share an immutable image, but only the runner
receives an explicit short-lived Kubernetes token; only the publisher references
S3 credentials. Inputs are immutable ConfigMap files mounted with `subPath`.
Do not commit tokens, credential values or pretend a synthetic test digest is a
published runtime selection.

## Admission and dispatch sequence

1. Check the lease-owned reservation and immutable execution binding first.
   An existing reservation restores its original plan, even if startup registry
   entries have changed. A bound executor is never recreated after disappearance.
2. For a new dispatch, read the actual selected cluster incarnation and exactly
   three nodes. Require reviewed names, placement, Linux amd64, Kubernetes
   v1.35.0, readiness, no pressure, allocatable budgets and a warm candidate image.
3. Require complete Metrics API observations with provider windows no longer
   than 60 seconds, timestamps at most 120 seconds old and no future samples.
   CPU/memory usage must meet profile thresholds. Pending/running/unknown Pods
   outside the reviewed namespace cohort reject admission, including another
   executor in `perf-generators`. Missing/denied/malformed data is not zero load.
4. Atomically reserve `perfeng-local` under the Run lease, revision and
   `PROVISIONING` state. Retain the first accepted six-check admission receipt
   and the exact immutable source plan in a closed, version-1 storage envelope.
   Unknown stored-plan versions fail closed. A competing Run receives a safe busy
   error without another principal's identity.
5. Create/adopt `perfeng-benchmark-<Run ID>`, then bind its actual UID durably.
   It must be Run-owned, restricted Pod Security v1.35 and not terminating. Once
   bound, an absent or replaced namespace is a conflict, never a create request.
6. Seal the exact closed runner request, including namespace and cluster UIDs,
   candidate, runner, workload, environment and Pod-template hashes.
7. Create/adopt quota, per-Run service account, narrowly scoped RBAC and immutable
   inputs. Recheck storage fencing and target UID before every API write. The
   target must have no Pods before a new executor is authorized.
8. Recheck live admission, lease/state/revision and namespace UID, then create
   or adopt the deterministic Job and persist its exact UID/spec fingerprint.

API calls never run inside storage transactions. A fence cannot atomically
cancel an API call already in flight; a racing effect remains attributed to its
retained reservation. Ambiguous Job creation is recovered by read-only exact
adoption before any new admission or materialization. Cancellation never creates
missing resources. The packaged runner performs its own measurement-time
qualification; accepted dispatch does not make later failed/unknown evidence
eligible for baseline comparison.

## Authority and operations

The executor namespace is `perf-generators`. Environment/runtime packaging must
explicitly grant the controller the required node/Pod/metrics reads and scoped
resource-creation authority. This PR changes no deployed role or runtime bundle.
Kubernetes RBAC cannot restrict namespace creation or Pod creation by label;
application checks and reviewed namespace placement are not a defense against
a compromised control-plane identity. Use a dedicated integration cluster and
do not grant this profile production authority.

The target quota caps ten Pods and aggregate CPU, memory and ephemeral-storage
limits. The runner may create Pods only in its Run namespace; its cluster role
has read-only observations/discovery and no Secret reads, namespace writes,
deletes, exec, preload or garbage-collection privileges. Existing mismatching
resources fail closed rather than being patched or deleted.

The reservation remains after worker expiry, cancellation, failed dispatch and
terminal lifecycle updates until verified cleanup commits. There is no public
release API, expiry sweep or force-delete recipe. Do not manually delete a hold
to unblock another Run. See [ADR-0007](decisions/0007-fence-and-verify-benchmark-cleanup.md)
for the safety trade-off and its supported local-cluster assumptions.

## Owned cleanup and restart recovery

1. Persist an irreversible cleanup marker under the current lease and Run
   revision. New materialization/dispatch is forbidden, including after restart.
   Cancellation saves `ABORTED`; execution failure saves its existing failure
   outcome. Normal completion preserves `COLLECTING` until raw evidence is
   durably verified, then gates analysis execution on cleanup.
2. Restore the original sealed plan. Inspect an ambiguously created namespace
   and bind only its exact owned UID. Recover an unbound Job with read-only
   matching lookup and immutable execution binding; never recreate it.
3. Close the exact owned ResourceQuota to zero Pods. Require both its spec and
   `status.hard.pods` to be zero. Retain that quota and namespace after release.
   If dispatch never created a namespace or request, confirmed absence is safe;
   a missing/replaced previously bound namespace remains a conflict.
4. Verify the supported singleton kubeadm API-server mutation timeout is at
   most one minute, stop the exact executor Job and record the first confirmed
   fence time in PostgreSQL. The two-minute drain begins here, even after a long
   worker outage. Reconciliation retries without blocking a worker for two minutes.
5. After that deadline, require the executor Job and all its dependent Pods
   absent and the original three nodes Ready with their selected fingerprints.
   Metrics, absence of pressure and a warm image cache are admission conditions,
   not cleanup conditions. Partitioned/unknown nodes never imply termination.
6. Delete only the ten recognized workload Pod names with their Run/benchmark
   labels, candidate image and SUT placement, and exact per-Run inputs, service
   account, Role/ClusterRole and bindings. Each delete uses UID/resource-version
   preconditions and normal graceful semantics. Foreign/modified objects and
   stuck finalizers retain the hold; a delete response is not absence.
7. Recheck ownership/absence and commit the bounded identity-bound receipt,
   release timestamp and lifecycle outcome atomically. Cancellation wins a race
   with the saved outcome. The reservation/audit row remains; another Run can
   reserve the logical target only after this commit.

Expired-worker and already-terminal retained holds are discoverable with fresh
leases. Terminal cleanup must not reopen or revise the public Run snapshot.
Registry edits cannot rewrite an old cleanup plan. The empty namespace and zero
quota are deliberate tombstones, not an incomplete successful cleanup. Do not
prune them with GitOps or remove them manually to make the cluster look tidy.

For diagnosis, inspect the original Run, selected cluster UID, persisted cleanup
phase/deadline, exact namespace/quota, executor Job/Pods and Ready node inventory.
For a held target, restore connectivity/readiness and retry reconciliation.
Ownership drift, missing bound namespaces or a replaced cluster require a
separate reviewed recovery decision, not reservation deletion or force release.
Receipts are internal coordination evidence, not a new public artifact format.

The containment claim assumes functioning quota admission/controllers, bounded
server requests, stable clocks and no privileged out-of-band changes. A lease
does not atomically fence a client paused before sending a request, and API
absence cannot prove physical termination under every partition. This is not
an exactly-once or general distributed-fencing protocol. Permanent zero quota
limits late workload creation; uncertain observations deliberately cost availability.

Runtime promotion must explicitly grant the controller quota get/create/update,
owned target-Pod get/list/delete, and per-Run authority/input get/delete in
addition to dispatch/read permissions. Review that privilege increase in the
dedicated integration cluster; no deployed role changes in this PR.

## Migration, rollback and verification

For an admission rejection or unknown observation, recapture qualification from
`perfeng-environment` without repairing or modifying the cluster:

```sh
uv run --locked python scripts/kubernetes_benchmark.py check --execute
```

That command prints each of the six check outcomes and retains a fresh receipt
and environment descriptor. Compare the observed cluster UID and fingerprint
with the selected entry as well as checking readiness, metrics availability,
usage budgets and active namespaces. A new receipt is diagnostic evidence, not
permission to replace a held Run's immutable plan or clear its reservation.

Migration `0017_benchmark_target_reservations.sql` adds a new table and unique
logical-target index. It does not rewrite or backfill existing Run data. The DDL
also locks the referenced Run table during foreign-key installation; allow a
bounded migration window rather than applying it during heavy lifecycle writes.
The normal migration ledger and transactional/advisory-lock protections apply.
Apply 0017 and 0018 before starting the new worker, even with no benchmark entry enabled:
recovery checks for retained holds before routing ordinary execution.

Storage operations preserve Run-before-lease lock order, read database time
after locks, and check lease loss before revision/state conflicts. Namespace and
request fields are write-once; target uniqueness does not include cluster UID,
so recreating a cluster does not evade an old hold. SQL additionally checks
bounded payloads, plan-content SHA-256 and request/namespace materialization order.

Migration `0018_benchmark_cleanup.sql` adds nullable cleanup phase, fence,
deadline, intended change, receipt and release fields. NULL means cleanup has
not begun; a missing fence means the quota barrier is not yet confirmed; receipt
and release remain NULL until verification commits. Existing holds remain held;
there is no backfill or Run snapshot rewrite. A partial unique index protects
only unreleased logical targets while retaining historical records.

Stop old reconciliation workers before rollout. Allow an ACCESS EXCLUSIVE
reservation-table lock, constraint validation and nonconcurrent index build;
inspect table size, contention, disk/WAL headroom and a migration window. Mixed
old dispatch/new cleanup workers are unsupported. Retain schema and audit rows
on application rollback; do not downgrade to pre-cleanup code with active holds
or benchmark dispatch enabled. Repair forward, never rewrite an applied migration.

Run `make verify`, `make validate`, `make db-validate` with disposable loopback
PostgreSQL, and `make kubernetes-validate` with the dedicated disposable test
cluster. Unit/fake tests prove rejection, ownership, least privilege and ordering;
real PostgreSQL tests prove uniqueness contention, expiry/takeover, persistence,
terminal cleanup recovery, atomic release/cancellation, upgrade and constraints.
Live Kubernetes tests prove API defaulting, immutable-object adoption, confirmed
quota rejection and UID/resource-version deletion without running the benchmark image.
