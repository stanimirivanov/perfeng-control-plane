# ADR-0006: Reserve benchmark targets before dispatch

- Status: Proposed
- Date: 2026-10-14
- Milestone: M04 — Kubernetes Benchmark Decision Loop
- Deciders: Repository owner (pending review)
- Supersedes: None
- Superseded by: None

## TL;DR

- Persist a logical-target hold before the first namespace write.
- Keep target ownership independent of expiring worker ownership.
- Bind namespace incarnation and seal exact input bytes before executor authority.
- Retain ambiguous effects and holds until a separate scoped cleanup proves release safe.

## Context

The contained kube-burner adapter creates benchmark Pods outside its executor
Job's ownership tree. Job de-duplication and an expiring reconciliation lease
alone cannot prevent overlapping measurements or safely release a dirty target.
Namespace names are reusable; a name does not establish incarnation ownership.

## Decision

Use a new PostgreSQL reservation keyed uniquely by logical target and Run. Its
immutable intent includes the observed cluster UID and complete approved plan.
Fresh admission precedes reservation and is rechecked before Job dispatch.
Namespace UID and exact final request bytes are write-once materializations.
All writes use existing Run-row/lease/revision/state fences; no database
transaction encloses a Kubernetes API call. Kubernetes objects are exact
create/adopt only, with deterministic names and safe conflict errors.

Recovery restores persisted source intent instead of re-resolving a changed
registry. Bound Jobs are never recreated. Cancellation may recover and stop an
exact executor, but remains `CANCELLING` and retains the hold until the later
target-cleanup capability establishes that executor and benchmark resources are
gone. The first slice exposes no release, steal or expiry operation.

## Alternatives considered

- Worker lease as target ownership: simpler, but expiry could admit another
  Run while the first Run's Pods still execute.
- Namespace labels only: useful diagnostics, but not durable exclusivity,
  principal isolation or protection against name reuse.
- Target plus cluster UID as the uniqueness key: permits a cluster-recreation
  path to bypass an unresolved logical-target hold.
- Immediate generic cancellation completion: incorrectly equates Job Pod
  termination with cleanup of independently created benchmark Pods.

## Consequences

Retries and worker takeover preserve the original target incarnation and sealed
inputs. Partial effects remain attributable and competing Runs remain excluded.
Safety reduces availability: unresolved holds deliberately require the follow-up
scoped cleanup/recovery work. Fresh integration admission is not a claim of
production calibration. Controller creation privileges require a dedicated,
reviewed deployment; application-level fencing cannot stop an already in-flight
external API effect or defend against a compromised controller credential.

## Compatibility, migration and validation

The additive migration retains old API and non-benchmark behavior. OCI kube-burner
registry entries now require explicit contained admission; no deployment bundle
is promoted by this decision. Rollback retains the table and held evidence.
See [the dispatch guide](../kubernetes-benchmarks.md) for source pins, operations,
lock/rollout considerations and required unit, race, PostgreSQL and live API gates.
