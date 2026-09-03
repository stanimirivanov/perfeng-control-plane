# Duplicate-safe Kubernetes Job dispatch

The Kubernetes dispatcher establishes one execution identity per Run before a
long-running reconciliation worker is introduced. A Run ID is also its Job name.
Retrying a Run therefore means reconciling the same Job; executing the workload
again requires a new Run ID.

## Create or adopt

`Dispatcher.EnsureJob` accepts a Job assembled from trusted, resolved catalogue,
environment and policy data. It does not accept Kubernetes manifests from the
public run API.

The dispatcher:

1. validates the fixed Run identity and a narrow at-most-once Job policy;
2. applies control-plane ownership labels to the Job and Pod template;
3. hashes the requested Job specification and records that fingerprint;
4. creates the deterministic Job; or
5. after `AlreadyExists`, reads and adopts only a matching owned Job.

An existing Job with another owner, fingerprint or requested specification
returns `ErrJobConflict`. It is never deleted, patched or replaced. This makes a
retry safe after an ambiguous create response: if the API server stored the Job
but the worker did not receive the response, the next attempt adopts it.

The comparison removes server-generated selector identity and the documented
Job and Pod defaults produced by the tested Kubernetes version, then requires
semantic equality. It does not ignore arbitrary additional or changed fields.
Kubernetes-assigned Job UID is returned with the expected specification
fingerprint as the durable cluster-side identity for subsequent observation.

An opt-in integration test creates an isolated namespace against a real API
server and verifies create, replay adoption and observation after defaulting. Set
`PERFENG_TEST_KUBECONFIG` to run it locally. CI runs kind v0.31.0 against
Kubernetes v1.35.0 using a digest-pinned node image. When Kubernetes dependencies
or the cluster version change, review the canonical defaults and rerun this test;
do not broadly discard unknown fields.

## Observe and stop

`ObserveJob` reads by deterministic name, then verifies namespace, Run label,
manager label, UID, recorded fingerprint and a newly calculated fingerprint of
the stored specification. Only then does it report `PENDING`, `RUNNING`,
`SUCCEEDED` or `FAILED`. A missing Job is reported as `ABSENT`; this is an
execution fact, not by itself a successful or failed Run outcome.

`RequestJobStop` performs the same identity checks and submits foreground
deletion with the exact Job UID as a precondition. A replacement Job is never
deleted, including when replacement occurs between the read and delete. An absent
Job makes the request idempotently successful. Observation also reports whether
deletion is in progress.

Neither accepted deletion nor `ABSENT` proves all workload processes stopped.
Foreground garbage collection waits only for known blocking dependents. A future
worker must verify owned Pod termination and handle orphaned resources, node
partitions and stale in-flight creation before moving a cancelled Run to
`ABORTED`. Durable stop intent must also prevent reuse of the deleted Job name.
These methods do not complete that cross-system cancellation protocol.

See [Kubernetes foreground garbage collection](https://kubernetes.io/docs/concepts/architecture/garbage-collection/#foreground-deletion).

## Required execution policy

Accepted Jobs have:

- exactly one completion and one-way parallelism;
- `backoffLimit: 0`, so Kubernetes does not repeat a load test automatically;
- a positive active deadline and `restartPolicy: Never`;
- no Pod service-account token, host namespaces or host-path volumes; and
- digest-pinned images for every init and ordinary container.

These checks are the duplicate-safety boundary, not complete workload admission.
The future resolver remains responsible for approved images, commands, target,
resource limits, node placement, storage credentials and policy. The future
worker must use its database lease for lifecycle writes, observe the returned
Job UID, and handle cancellation without deleting unrelated Jobs.

## Failure behavior

Caller cancellation and deadlines retain their standard Go error identity.
Kubernetes timeouts, throttling and service/internal failures map to
`run.ErrUnavailable`. Other API errors expose only the operation and Kubernetes
status reason; server messages are not returned. If `AlreadyExists` is followed
by `NotFound`, the caller retries because the identity changed during observation.

The package performs no polling, lifecycle transitions, log reads, artifact
collection or automatic retries. Those are separate reconciliation slices. Unit
tests use an injected narrow Job client and cover initial creation, matching
adoption, ambiguous responses, concurrent reconciliation, status observation,
UID-safe cancellation, conflicts, validation and error safety.
