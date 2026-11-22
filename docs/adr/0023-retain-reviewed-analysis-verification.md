# ADR-0023: Retain reviewed analysis verification

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Capture optional reviewed verification with native admission in one transaction.
- Preserve exact policy bytes and historical producer/bundle evidence on replay.
- Keep absence explicit and prohibit augmentation from current registry state.

## Decision and consequences

Migration 0022 adds nullable immutable analysis_verification to integration_requests.
Its private JSON representation uses base64 policy bytes to preserve the exact hash
input alongside expected producer and concrete bundle version. The existing native
capture callback returns this optional evidence; validation against the native
request precedes insertion. Invalid capture rolls back Run, request and event.

Database guards require native association, bounded policy bytes matching the native
SHA-256, snapshot shape and concrete version. They prohibit mutation or augmentation;
existing native guards also retain parent context. This does not make direct SQL an
alternate policy API or establish authenticated provenance by itself.

Scoped reads return owned evidence and revalidate its consistency with the retained
native request. Replay precedes review and never uses current registry or approval.
NULL preserves legacy/custom absence indefinitely, including mixed-version writers.
Existing publication upgrade fixtures seed historical columns directly so they test
old data without running a new binary against an unmigrated database.

Apply the additive migration before deployment; retain schema/evidence on application
rollback. Existing table grants suffice. No wire contract, endpoint, publication
activation, fetching or delivery changes. Historical publication composition is the
next consumer and must fail closed when reviewed evidence is absent.
