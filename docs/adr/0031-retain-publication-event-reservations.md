# ADR-0031: Retain publication event reservations

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Retain one immutable event/time per scoped request and registered report/candidate.
- Allocate only after the existing request lock, using database time and random ID.
- Recover verified publication identity without retrospective reservation/backfill.

## Decision and consequences

Add an inward reservation storage contract and additive migration 0023. The new
table binds provider/principal namespace, scoped request and registered native
report/candidate IDs. Database constraints/triggers require reviewed native admission,
correct artifact ownership/formats and a finite timestamp after admission. Mutation,
deletion and truncation are forbidden. A reservation grants no publication authority.

The bounded allocator uses the same principal/request advisory key as admission
and publication; it acquires no publication event lock, so introduces no reverse
lock order. Read existing reservation and verified publication before allocation.
Matching reservation replay returns original event/time; changed evidence conflicts
and unknown/invisible scope returns NotFound. Read the database clock after locking
and generate an opaque random ID only for a new insert. No external I/O, current
registry review or artifact byte reads occur inside the transaction.

An existing verified publication is authoritative: return its original event/time,
reject a different report or conflicting reservation, and never backfill candidate
association from publication claims. Commit failure is uncertain; retry identical
scope/evidence. Future materialization still verifies exact bytes and frozen policy.
Custom publication writers must coordinate stable identities; incompatible verified
publication/reservation history fails closed rather than being rewritten.

Apply 0023 and trusted runtime SELECT/INSERT grants before enabling the later app
resolver. No existing rows are rewritten/backfilled and no runtime activates here.
Migration creates an empty table/indexes/triggers and takes normal short referenced
table constraint locks; deploy with the established bounded migration window.
Older binaries ignore the table. Roll back binary only, preserving all evidence;
repair operational failures forward, never delete a reservation to force a retry.
