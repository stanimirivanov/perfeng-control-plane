# ADR-0013: Retain correlated admission atomically

- Status: Proposed
- Date: 2026-10-24
- Milestone: M09 — Perfeng integration
- Deciders: Repository owner and integration consumers (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Persist request and delivery identities independently of native key expiry.
- Commit the accepted Run, immutable disposition and delivery digest together.
- Hold baseline approval locks through the admission transaction.

## Decision and alternatives

Add migration 0019 with principal-scoped request and delivery ledgers. Retain
accepted and rejected dispositions indefinitely. Reusing context/event identity
with different content or reusing an attempt conflicts. Exact replay bypasses
new approval and deadline checks, preserving the original disposition and Run.
New delivery IDs attach to that disposition without redispatch. Reads join the
current owned Run without changing the stored acceptance response.

Reusing native 24-hour create bindings would permit duplicate execution after
expiry. Checking approval outside the transaction would allow retirement between
validation and acceptance. Instead inject a trusted in-process review function
with a transaction-bound, explicitly principal-scoped `FOR SHARE` baseline reader.
Review must perform no other I/O. Existing native admission is unchanged.

## Transactions, compatibility and recovery

Lock order is delivery advisory scope, request advisory scope, baseline share
locks, then new Run/ledger writes. No existing Run is locked during admission.
Read database time after waiting and again after review; guard the Run insertion
with the deadline too. Baseline locks remain held until commit. Temporary review
or storage errors roll back; permanent selection failures and expired deadlines
produce safe durable rejections. A request timestamp ahead of the DB clock is
invalid and reserves no identity. Wire validation/authentication are caller duties.

Apply 0019 before enabling consumers; it adds empty tables and leaves old Runs,
native keys and migration bytes unchanged. Grant runtime SELECT/INSERT on both
new ledgers. Old binaries ignore them; rollback keeps tables and retained rows.
An uncertain commit is resolved by identical retry or scoped GET, never a new
request ID. No expiry, deletion or automatic down migration is introduced.

## Verification and follow-up

Live tests cover empty/repeat migration, upgrade with existing Run data,
concurrent replay, reopen, conflicts, attempt uniqueness, rejected/transient
outcomes, principal scope, current Run reads, baseline contention and deadlines.
Run `make validate` and isolated `make db-validate`. Authenticated transport and
composition follow in PR6c2; cancellation, retirement/tombstones and lifecycle
projection remain PR6d. This storage slice exposes no HTTP routes.
