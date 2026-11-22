# ADR-0033: Support Linux development and native browser hosts

- Status: Proposed
- Date: 2026-10-31
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner
- Supersedes: Earlier Windows host-support commitments
- Superseded by:

## TL;DR

- Linux is the supported development, CI and native browser host.
- Retire active Windows adapters and instructions while preserving browser execution.
- Historical contracts, migrations, bundles and evidence retain their original meaning.
- Fresh Linux qualification and coordinated release selection precede activation.

## Context

The owner is moving Perfeng development to Linux and explicitly requested removal
of Windows support while retaining native browser execution on Linux.

## Decision

Use Linux CI and operator commands, POSIX process groups and account-private
filesystem permissions. Replace active Windows runtime assumptions with explicit
Linux identity. Version incompatible protocol changes instead of relabeling
previous evidence. Historical ADRs and published artifacts remain reproducible;
this decision supersedes their host-support commitments. Universal dependency
locks and tests rejecting foreign paths remain intact.

## Alternatives

Dual-host support retains unused CI and adapters. Deleting browser execution
would abandon the requested native browser decision loop. Rewriting historical
identities would invalidate provenance and comparison boundaries.

## Consequences and compatibility

Windows is no longer an active supported host. Linux correctness tests do not
qualify a workstation or make its measurements comparable with Windows results.
M03 needs fresh reviewed host evidence, immutable runtime selection and complete
live decision-loop acceptance. A Linux worker must use compatible reviewed API
and control-plane releases; no automatic promotion occurs.

## Security and operations

No cluster, credential, persistent-data or release mutations are part of this
source migration. Existing evidence remains unchanged. Restore source support
only through a reviewed revert or follow-up decision; never relabel retained data.

## Validation

Run the repository's Linux verification gates and focused platform rejection,
process cleanup and contract checks. Report missing prerequisites and unexecuted
live qualification explicitly. Preserve existing Linux security and integrity gates.
