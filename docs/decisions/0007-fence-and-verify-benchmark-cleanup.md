# ADR-0007: Fence and verify benchmark cleanup before releasing a target

- Status: Proposed
- Date: 2026-10-14
- Milestone: M04 — Kubernetes Benchmark Decision Loop
- Deciders: Repository owner (pending review)
- Supersedes: None; completes the cleanup capability deferred by ADR-0006
- Superseded by: None

## TL;DR

- Persist an irreversible dispatch cutoff before cancellation or cleanup.
- Keep the empty Run namespace and its zero-Pod quota as safety tombstones.
- Drain bounded requests, verify executor/workload absence and revoke only
  exact owned authority before atomically releasing the logical target.
- Preserve raw collection on normal completion; cleanup gates analysis.
- Recover retained terminal holds without reopening their public lifecycle.

## Context

The kube-burner executor creates independent Pods outside its Job ownership
tree. Deleting that Job, expiring a worker lease or advancing a Run to a
terminal state does not establish target cleanup. A crash can also leave a
namespace or Job created but not yet bound in PostgreSQL.

External requests cannot be enclosed in the Run transaction. An immediately
deleted namespace or quota would remove the admission barrier against late
Pod creates. Cleanup must trade availability for retained evidence whenever
termination, ownership or the selected cluster incarnation is uncertain.

## Decision

Store an irreversible cleanup marker, intended lifecycle outcome, confirmed
fence time, database-time drain deadline and release receipt on the reservation.
Materialization refuses that marker. Repeated attempts restore the original
plan rather than consulting a changed registry. Read-only recovery may bind an
ambiguous namespace/Job UID; it must not create an executor or workload authority.

Close the exact owned quota to zero Pods and require its status to confirm that
limit. Retain both quota and namespace permanently in this capability. The
reviewed single kubeadm API server must have its default one-minute mutation
request timeout or an explicit positive timeout no greater than one minute.
Wait two minutes from the first confirmed barrier using PostgreSQL time, not a
sleep or worker-local clock. A worker outage before confirmation cannot consume
that drain period.

Request foreground deletion of the identity-checked executor. After the drain,
require its Job and dependent Pods absent, and the original three nodes Ready
with the selected incarnation fingerprint. Cleanup need not pass admission's
pressure, image-cache or metrics checks: it must be able to relieve pressure.
Delete only recognized workload Pods and exact per-Run inputs, service account
and RBAC, using UID and resource-version preconditions. Never force-delete Pods,
delete a namespace, clear finalizers or delete a collection. A successful delete
response is not observed absence.

Release retains the original reservation and verification receipt as audit data.
A partial unique index protects unreleased logical targets. The Run lock and
current lease/revision fence serialize release with cancellation and competing
workers; cancellation overrides the saved outcome. Already-terminal recovery
preserves the exact public Run snapshot. No public release or force-unlock API
is added.

Normal completion first collects and verifies durable raw artifacts through the
existing lifecycle. Cleanup gates entry to analysis execution, preserving the
capture needed for collection. Cancellation and infrastructure failure enter
cleanup directly; ambiguous/failed cleanup retains the hold for rediscovery.

## Alternatives considered

- Delete the Job and release immediately: independent benchmark Pods and
  ambiguous creates survive, so target reuse is unsafe.
- Delete the whole namespace: simpler garbage collection, but too broad and
  removes the durable admission barrier.
- Expire target reservations with worker leases: admits overlapping Runs after
  a worker outage.
- Keep every reservation forever: safe but prevents subsequent benchmark Runs.
- Add a general admission webhook or distributed workflow engine now: broader
  infrastructure and operational scope than this contained local profile.

## Consequences and limits

Recovery is retryable across process and lease changes without redispatch.
Empty namespaces/quotas and audit rows accumulate deliberately; any later
tombstone retirement needs a separately reviewed safety protocol.

This is conditional containment for the reviewed local integration cluster,
not proof of physical process termination during a partition or exactly-once
execution. It assumes functioning API-server quota admission/controllers,
bounded server requests, stable clocks and no privileged out-of-band changes.
Leases cannot atomically fence a paused client between its last ownership check
and an API request. Retained zero quota limits late workload creation; missing
nodes, identity drift or unknown observations still prevent release. Unbounded
pauses, clock jumps or compromised administrators are not solved by this design.

## Compatibility, migration and operations

Migration 0018 expands reservations with nullable cleanup fields and replaces
permanent target uniqueness with uniqueness of active holds. Existing holds
remain unreleased without backfill. The ALTER TABLE requires an ACCESS EXCLUSIVE
lock; constraint validation and nonconcurrent index creation scan the table.
Inspect size, lock contention, disk/WAL headroom and a maintenance window.

Stop old reconciliation workers before applying the migration and starting the
new binary. Migration 0018 is required even with no benchmark entries, because
claim discovery inspects retained holds. Do not mix old dispatch-capable workers
with cleanup workers or downgrade to pre-cleanup code while active holds remain.
Keep the schema/audit data and repair forward; no down migration is provided.

The later runtime-promotion PR must review scoped cleanup RBAC explicitly. This
change neither deploys broader permissions nor promotes an image or bundle.

## Validation

Run the complete non-live harness, real disposable PostgreSQL migration,
concurrency and restart tests, and the disposable Kubernetes API integration
gate. Tests cover cutoff, drain retries, cancellation priority, terminal
rediscovery, concurrent release, foreign resources, stuck Pods, lost leases,
node uncertainty, quota status and real API quota rejection/preconditioned
revocation. Live milestone acceptance remains separate from this source PR.

The server timeout and quota semantics are reviewed against Kubernetes v1.35.0
[server configuration](https://github.com/kubernetes/kubernetes/blob/v1.35.0/staging/src/k8s.io/apiserver/pkg/server/config.go)
and [quota admission](https://github.com/kubernetes/kubernetes/blob/v1.35.0/staging/src/k8s.io/apiserver/pkg/admission/plugin/resourcequota/resource_access.go).
