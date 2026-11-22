# ADR-0015: Recover integration cancellation from durable state

- Status: Proposed
- Date: 2026-10-24
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Bind authenticated cancellation to the retained immutable request context.
- Reuse native cancellation and termination confirmation without inventing success.
- Recover deadlines independently of HTTP traffic and reconciliation capacity.
- Retain correlation and delivery ledgers indefinitely; introduce no deletion.

## Decision

POST the published cancellation path with `runs:cancel`. Verify configured
principal identity, schema, path/query request identity and exact stored context.
Serialize event then request advisory scopes before locking the mapped Run.
Persist the cancellation event digest and Run transition in one transaction.
Unknown/invisible requests return 404; rejected requests and conflicting context
or delivery reuse return 409. No cancellation creates an execution or placeholder.
An admission holding the request lock commits before cancellation can resolve it;
an unknown request requires retry/reconciliation of its original admission.

CREATED/VALIDATING transition directly to ABORTED; later active work transitions
to CANCELLING. Return 202 only for CANCELLING; terminal snapshots return 200.
Repeated cancellation preserves revisions. A concurrent terminal completion wins
under the same Run lock and remains immutable. Admission replay remains original.

Run deadline recovery at startup and every configured worker poll interval,
independently of the reconciliation worker's active capacity. Each transaction
selects at most 100 overdue active mapped Runs using DB time and SKIP LOCKED,
then rechecks DB time under the Run locks before native cancellation. No external
calls occur in these transactions. Existing reconcilers/native lease handling
confirm external termination; deadline expiry is never such confirmation.

## Operations and compatibility

Enable the supervised recovery loop only with reviewed integration bindings and
a recovery-capable store. Report classified failures through the existing worker
reporter and retry subsequent polls. Restart resumes from durable state; multiple
instances skip locked Runs. No migration, dependency or credential change is added.
Existing Run UPDATE and integration event INSERT grants are sufficient.

Keep ledgers indefinitely rather than adding retirement/deletion semantics.
Disable bindings before application rollback and keep retained mappings/digests.
No request can silently become new after native create-key expiration. Snapshot
projection and revisions preserve ADR-0014 and the pinned shared contract.

## Verification

Run HTTP scope/permission/protocol tests, supervised recovery tests and live
PostgreSQL replay, conflict, deadline, row-contention and restart tests; run
`make validate` and `make db-validate`. Live execution APIs are unchanged.
