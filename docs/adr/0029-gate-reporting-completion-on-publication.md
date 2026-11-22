# ADR-0029: Gate reporting completion on opt-in publication

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Add a callable opt-in publication gate after report registration, before completion.
- Recover from publication or advance failures using the registered report.
- Keep the existing constructor, runtime composition and native lifecycle unchanged.

## Decision and consequences

Add `ReportPublisher` and `NewPublishingReportingReconciler`. The publisher receives
the worker principal, an owned native Run and selected validated candidate/report
references. It must establish request authority independently from retained admission
and configured namespaces. Native Run metadata is not public publication authority.
Nonparticipating Runs may complete when the trusted publisher returns nil.

Both newly registered and recovered reports pass through the gate before completion.
Publication errors remain operational/retryable and never become report-process
failure or a performance verdict. Completion still uses the original lease and
revision; a concurrent cancellation/revision wins through existing fenced storage.
Check attempt cancellation before and after publication. No database transaction
spans the external work; the existing reporting attempt deadline bounds the gate.

Registration and publication are intentionally separate commits. A failed/uncertain
publication retries with the durable report, without generating or registering it
again. Successful publication followed by uncertain completion may invoke the gate
again: the publisher must choose stable identities and recover retained replay.
This seam does not itself promise exactly-once external execution.

The existing constructor preserves behavior for all current callers. No runtime
activation, migration, wire contract, permissions or dependency is added. A later
application adapter will join scoped native admission lookup and replay-first
materialization, with stable event selection/recovery reviewed before activation.
Rollback disables the opt-in composition without removing registered reports or
retained publications. A publication outage can delay completion when enabled;
operations must provision an appropriate existing attempt timeout.
