# ADR-0001: Use expiring capabilities for native execution ownership

- Status: Proposed
- Date: 2026-09-25
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Native execution uses database-fenced, expiring leases rather than process
  memory or a self-contained token.
- The worker receives random bearer authority once; PostgreSQL stores only its
  SHA-256 digest.
- Acquisition locks worker presence before the Run and uses the database clock
  to recheck freshness, capacity and ownership.
- One current row per Run is replaced after expiry, giving stale attempts no
  authority while keeping crash recovery simple.
- Acquisition remains coordination-only and does not advance the Run lifecycle.

## Context

Qualified Windows workers pull browser work from the control plane. Multiple
service replicas and workers can concurrently observe the same CREATED Run, and
a worker process can stop after receiving work. Ownership must survive service
restarts, expire without process-local cleanup, prevent two workers from owning
one Run and enforce the Native Worker API v1 capacity of one active lease per
worker.

The lease capability crosses an authenticated HTTP boundary in a later slice.
It is secret bearer material and must not appear in JSON, logs, evidence or
plaintext database state. Registry plan resolution can involve configuration
and must not run inside a database transaction.

## Decision

Persist one current native execution lease per Run. Each acquisition creates
new random version-4 lease and attempt UUIDs, a 256-bit random capability, an
issue time, expiry, renewal interval and the reviewed execution plan. Persist
only SHA-256 of the capability. An expired row may be replaced atomically with
new authority; a live row may not.

Scheduling reads an eligible presence observation and resolves compatible plans
before acquisition. The final transaction locks the worker-presence row before
the selected Run row, reads the database clock and rechecks:

- the exact presence observation and its freshness;
- availability, qualification and acquisition authorization;
- compatibility with the reviewed plan;
- one-active-lease worker capacity;
- native execution target, CREATED state and Run revision; and
- absence or expiry of competing Run ownership.

Acquisition does not change the Run revision or lifecycle state. A later
lease-renewal operation will own the first execution lifecycle transition. This
allows an acquisition that expires before preparation to be planned and leased
again without inventing a lifecycle rollback.

## Alternatives considered

### Store plaintext capabilities

- Benefits: Simple equality checks during renewal.
- Costs and risks: A database read or diagnostic dump would disclose active
  bearer authority.
- Reason not selected: Hash comparison provides the required verification
  without retaining reusable secret material.

### Use signed self-contained lease tokens

- Benefits: Some ownership checks could avoid a database read.
- Costs and risks: Revocation, capacity enforcement, expiry recovery and Run
  revision fencing still require durable shared state, while signing-key
  rotation adds another operational secret.
- Reason not selected: It does not remove the transaction needed for correct
  ownership and creates unnecessary key-management complexity.

### Keep append-only attempt history in the lease table

- Benefits: Direct database audit history for every expired attempt.
- Costs and risks: Active-row uniqueness becomes more complex because expiry is
  clock-dependent, and later evidence tables should own accepted attempt history.
- Reason not selected: The lease table represents current authority. Durable
  accepted artifacts and terminal reports will preserve relevant evidence.

### Advance the Run during acquisition

- Benefits: The public lifecycle would immediately show that work was handed out.
- Costs and risks: A worker can disappear before preparation, leaving recovery
  to invent a backward transition or lease a Run whose plan resolver no longer
  accepts its state.
- Reason not selected: Acquisition proves ownership, not execution progress.

## Consequences

### Positive

- Concurrent service replicas share one durable ownership decision.
- Stale workers lose authority after expiry and receive new identities on retry.
- Worker capacity and per-Run exclusivity are enforced in one transaction.
- Registry and other external work stay outside database transactions.
- Database contents do not contain reusable lease capabilities.

### Negative

- Renewal and every privileged mutation require a database lookup and digest
  comparison.
- The current lease table does not retain expired attempt history after a Run is
  reacquired.
- All native lease operations must preserve the worker-before-Run lock order.

### Neutral or follow-up

- Renewal, release, artifact registration and terminal reporting must bind the
  worker, lease ID, attempt ID, capability digest and expected Run revision.
- Accepted attempt/evidence history belongs in the later artifact and terminal
  persistence model, not in current coordination state.

## Compatibility and migration

Migration `0007_native_execution_leases.sql` only adds a table and index. It does
not rewrite Runs or presence rows, so the previous application version can run
against the expanded schema. Rollback uses the previous binary while retaining
the unused table; recovery is forward through another migration rather than by
editing or removing migration 0007.

## Security and operations

Capabilities are generated from the operating-system cryptographic random
source, excluded from JSON and diagnostic formatting, and hashed before the
transaction writes them. A failed or uncertain commit returns no capability;
any committed but unreachable lease becomes recoverable after expiry.

The runtime role needs SELECT, INSERT and UPDATE on the new table. The table and
scheduler are privileged control-plane boundaries and are never exposed to
tenant SQL callers. Operators must use the database clock and monitor repeated
expiry/reacquisition once runtime metrics are introduced.

## Validation

- Verify fresh, qualified and authorized presence eligibility.
- Verify capability hashing and JSON/diagnostic redaction.
- Race two workers for one Run and observe exactly one winner.
- Verify one-active-lease capacity per worker.
- Expire and reacquire a lease with new lease, attempt and capability authority.
- Apply the full migration chain to empty and upgraded disposable PostgreSQL.
- Run formatting, race, fuzz, module and vulnerability checks.
