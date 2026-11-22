# ADR-0030: Compose scoped reporting publication

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Join the reporting gate to scoped native admission and historical materialization.
- Replay retained event/time without identity allocation or object reads.
- Require an explicit trusted durable event resolver; supply no ephemeral default.

## Decision and consequences

Add a callable application `ReportingPublisher` implementing the reporting port.
Configured provider/principal namespace and authenticated worker principal establish
scope. Resolve the native Run to retained admission, checking full scope and native
request equality; use the retained request identity and selected artifact IDs.
Native Run metadata cannot select public request authority or historical review.

Unknown/unassociated Runs and historical admissions without native/verification
evidence are nonparticipating and may finish ordinary reporting without publication.
Malformed or substituted associated evidence fails closed. Read retained publication
before requiring historical verification: recover its original event/time and
delegate canonical replay to the existing workflow without event resolution or
object access. Even after an uncertain write, retry does not generate new identity.

For new publication inject a trusted `PublicationEventResolver`. It receives scoped
request/artifact IDs, not caller event/time, and must durably preserve identical
identity/time across concurrent calls and restarts, rejecting changed reservations.
No clock/random default is safe. Existing preparation, builder and atomic verifier
still own registered evidence, exact bytes, policy/provenance and transaction checks.
External reads and resolver work remain outside materialization transactions.

This is callable composition only. Durable identity allocation and runtime activation
remain separate slices; do not enable an ephemeral resolver in deployment. No schema,
wire, permissions, dependencies or registry lookups change. Nil object reader allows
retained replay only. Backend failures remain safe/classified, cancellation is
preserved and no failed call completes reporting. Rollback removes the composition
without deleting registered evidence or retained publications.
