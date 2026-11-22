# ADR-0037: Scope worker rerun selection to immutable child identity

- Status: Proposed
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Give trusted workers an exact child/owner/request lookup for retained rerun inputs.
- Preserve historical evidence independently from current interactive read grants.
- Require separate lease/current-admission checks; do not activate execution yet.

## Decision and consequences

M05-PR59a / issue #336 adds the worker lookup described in
[rerun persistence](../rerun-persistence.md#worker-selection-lookup-m05-pr59a).
Reject using actor-facing receipt recovery as a worker selector: a revoked human
read grant must not rewrite historical inputs. Also reject lookup by newest intent,
source alone or an inferred principal prefix. Validate the exact child, native
owner, request and immutable service/creator lineage before returning evidence.

The operation is a trusted storage capability, not a new authorizer or public
read. Current dispatch eligibility, evidence access and lease ownership require
explicit checks in the future consuming adapters. Returning an old APPROVED
snapshot proves recorded history only. No application caller is added; dispatch,
reporting and collection qualification must finish PR59 before reruns are enabled.
Rollback removes the unused lookup while retaining existing schema and receipts.
