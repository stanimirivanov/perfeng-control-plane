# ADR-0049: Bind policy applicability by reviewed test selection

- Status: Accepted
- Date: 2026-11-22
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Workspace owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Test ID and Performance Policy ID identify different resources.
- Reviewed registry bindings establish policy applicability for execution.
- Analysis still requires equal names. That compatibility gap needs a separate implementation change.

## Context

The [report-policy registry](../report-policy-registry.md) already retains the Test ID separately from the policy ID, version and exact-byte digest. The current Analysis evaluator requires the result’s Test ID to equal the policy name. For example, the registry may bind `checkout-api` to `checkout-latency`, but the current evaluator rejects that pair.

## Decision

The reviewed registry binding determines whether a policy applies to a test in its reviewed execution context. Report-time evidence retains the exact selection. An evaluator must receive the authenticated or otherwise independently verified applicability evidence appropriate to its boundary before it can support differently named pairs.

## Alternative and trade-off

Requiring equal names everywhere gives a standalone evaluator a simple local check. It couples two identities, forces naming collisions or coordinated renames and cannot express the rest of the reviewed applicability conditions. Explicit association preserves separate versioning and shifts a real verification obligation to the caller and consumer boundary.

## Compatibility and migration

Keep the current Analysis equality check until the caller and evaluator interfaces can carry and verify the reviewed association. Existing equal-name selections remain supported. This ADR records the decision and the current gap.

## Validation

A future implementation must prove that a differently named pair evaluates with the exact reviewed binding and that an unreviewed pair, mismatched digest or cross-context substitution is rejected. Replay must preserve the original report-time selection.
