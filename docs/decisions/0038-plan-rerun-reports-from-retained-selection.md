# ADR-0038: Plan rerun reports from the retained source selection

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Share one frozen rerun plan between report jobs, report approval and schema selection.
- Bind the actual registered child candidate without reselecting policy or baselines.
- Keep known-rerun failures out of ordinary fallback; dispatch remains a later gate.

## Decision and consequences

M05-PR59b / issue #338 follows [frozen report planning](../rerun-persistence.md#frozen-report-planning-m05-pr59b).
Use a consumer-owned association/read capability that distinguishes ordinary Runs
from missing evidence inside a known rerun. Preserve the existing child lookup;
add an explicit association observation instead of inferring execution identity
from a principal prefix or treating every not-found as permission to reselect.

Adapt only the candidate to its independently registered child artifact. Frozen
policy, workload, report producer, schema pin and exact baseline evidence feed
both report Job generation and verdict verification. Preserve deterministic input
hashing through the existing report capture annotation. Do not duplicate mutable
current approval into this plan or create a competing report/verdict model.

The existing report-capture flag owns composition. Disabled mode is unchanged;
enabled deployments must already have migration 0027 and read privileges. This
adds database reads for ordinary captured Runs but leaves their planner behavior
unchanged. No new dependency, schema migration or public contract pin is needed.
Rollback preserves accepted evidence and disables/reverts the new composition.
Dispatch/admission and normalization qualification still precede public rerun use.
