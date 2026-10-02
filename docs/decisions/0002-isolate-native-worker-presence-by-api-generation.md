# ADR-0002: Isolate Native Worker presence by API generation

- Status: Proposed
- Date: 2026-10-02
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner (review pending)
- Supersedes: None
- Superseded by: None

## TL;DR

- Store v2 presence in a new table; leave v1 rows and their constraint unchanged.
- Keep the same authenticated worker identity and server-owned qualification decision.
- Do not treat a v2 report as v1 lease eligibility or enable v2 acquisition yet.

## Context

The v1 presence table enforces `protocolVersion: 1`, and v1 scheduling decodes
its rows as v1. Overwriting a row with a v2 report would make v1 scheduling fail
and would break a rollback to the previous binary. The v2 recipe requires a
v2-qualified worker before lease issuance can be safely enabled.

## Decision

Accept `POST /native-worker/v2/presence` with the pinned v2 presence contract.
Authenticate the same worker audience, compare its claimed qualification
evidence against the server-owned reviewed bytes, and keep qualification and
acquisition authorization distinct. Persist the latest v2 report in
`native_worker_presence_v2`, keyed by authenticated worker ID. V1 presence and
lease selection continue to read only `native_worker_presence`.

The v2 route is presence-only. Its response and durable row report
`authorizedToAcquire: false`, even for a credential authorized to use v1
acquisition. The database constrains that gate until a reviewed v2 leasing
migration explicitly changes it.

## Alternatives considered

### Widen the existing table's protocol constraint

- Benefits: One last-seen row and no second table.
- Costs and risks: Old binaries and v1 scheduling decode v2 rows as v1; rollback
  would require destructive row repair or tolerate failures.
- Reason not selected: It weakens the mixed-version deployment boundary.

### Reject v2 presence until v2 leasing is complete

- Benefits: No intermediate storage or endpoint.
- Costs and risks: Parsing, qualification, and persistence cannot be verified
  independently of the later lease transaction.
- Reason not selected: A presence-only slice is independently observable and
  can be deployed safely before acquisition is enabled.

## Consequences

### Positive

- Existing v1 reports and lease behavior remain unchanged across upgrade and
  rollback.
- V2 qualification can be observed before the worker is allowed to acquire.

### Negative

- The service retains one latest report per API generation for a worker.
- Future v2 scheduling must enforce capacity across both generations using the
  shared native lease authority; the presence tables alone do not do that.

### Neutral or follow-up

- Stale v1 presence may remain eligible for its bounded freshness window after
  a worker switches to v2. Its lease expires if the worker no longer polls v1.
- V2 acquisition, exact recipe persistence, and lease-scoped operations are
  separate changes and must not infer authority from this table alone.

## Compatibility and migration

Migration 0013 adds only an empty v2 table. It does not rewrite the v1 table,
Runs, leases, or evidence. The prior runtime binary ignores the new table.
Rollback is the prior runtime binary with the expanded schema retained; do not
rerun its older migration command, which rejects unknown ledger versions.
Forward repair uses another migration rather than dropping or rewriting 0013.

## Security and operations

The worker cannot select its worker ID, qualification, or acquisition policy.
The v2 report has the same bounded JSON and independent worker-audience
authentication as v1. Grant the runtime role SELECT, INSERT, and UPDATE on the
new table; do not expose it to tenant callers. A v2 report is not a lease and
contains no lease capability.

## Validation

Test cross-version rejections, qualification mismatch, exact authenticated
identity, v1 scheduling isolation, empty-schema migration and upgrade from an
existing v1 presence row on disposable PostgreSQL. Run the repository's normal
and race suites.
