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

The comparison treats API-server defaulted fields as additions while requiring
every field requested by the control plane to match. Kubernetes-assigned Job UID
is returned as the durable cluster-side identity for subsequent observation.

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

The package performs no polling, lifecycle transitions, cancellation, log reads,
artifact collection or automatic retries. Those are separate reconciliation
slices. Unit tests use an injected narrow Job client and cover initial creation,
matching adoption, ambiguous responses, conflicts, validation and error safety.
