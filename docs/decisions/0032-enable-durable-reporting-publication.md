# ADR-0032: Enable durable reporting publication explicitly

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Adapt durable event storage to the reporting publisher without ephemeral identity.
- Require an explicit switch and reviewed integration/storage capabilities.
- Publish before fenced completion; preserve default and replay behavior.

## Decision and consequences

Add `NewDurableReportingPublisher` over historical publication and event storage.
Map independently established scope and canonical report/candidate IDs exactly into
the inward reservation request. Return only retained event/time; failures expose no
partial identity or backend detail. Cancellation remains authoritative. Allocation
uncertainty retries the same reservation, while publication/advance uncertainty
recovers retained publication before allocation or object access.

`PERFENG_INTEGRATION_ANALYSIS_PUBLICATION_ENABLED` is an optional strict boolean,
default false. Enabled application composition requires enabled reviewed registry
namespaces, native/history/materialization/event storage capabilities and the
configured verified artifact reader. Missing prerequisites fail construction with
a safe configuration error. Construction starts no loops or external I/O.

Inject the publisher only into the opted-in reporting stage. After registration,
participating Runs retain verified publication before completion; nonparticipating
native-only/unsupported history keeps ordinary reporting. Existing attempt deadline,
lease/revision fencing and cancellation priority remain unchanged. Publication outages
can delay completion when enabled, so configure the existing timeout appropriately.

Deployment must apply migration 0023 and trusted runtime SELECT/INSERT grants before
enabling; startup does not apply migrations or prove database privileges. No new
migration, dependency, wire schema or tenant grant is introduced. This activates only
reporting-stage publication, with no terminal-history scan/backfill or external
delivery worker. Disable the switch or roll back binary to revert, preserving all
registered artifacts, reservations and publications.
