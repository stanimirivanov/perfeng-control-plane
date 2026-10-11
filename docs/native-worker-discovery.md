# Native Worker discovery

## TL;DR

- Use discovery when a lost acquisition response left the Run/attempt unknown.
- Stop the old worker/browser tree, exclude other users of the identity, and
  obtain acknowledgement of UNAVAILABLE before discovering.
- Discovery observes current v2 capacity and stored intent, not history or
  authority. NONE does not permit evidence deletion or automatic acquisition.
- Roll out all control-plane writers before enabling recovery clients; no
  database migration or new privilege is required.

## HTTP boundary

`POST /native-worker/v2/attempts/discover` requires a worker-audience bearer
credential authorized for native operations. The request is exactly
`{"protocolVersion":2}`; body selectors, extra fields and query parameters are
rejected. Run Management credentials cannot select a worker. The operation
remains available when the global new-acquisition gate is disabled, just like
known-attempt inspection. Body-size, authentication and classified dependency
failures use the existing HTTP policy.

A successful response has `Cache-Control: no-store`, one `serverTime`, stored
`availability`, and `state` NONE or FOUND. UNREPORTED means no presence exists;
it is not fabricated UNAVAILABLE intent. Availability is independent of report
freshness, qualification and acquisition authorization.

FOUND includes `slotExpiresAt` and the non-secret ACTIVE or TERMINAL inspection
of the single owned, unreleased v2 lease expiring strictly after `serverTime`.
Nested and envelope clocks agree; ACTIVE expiry equals slot expiry. A matching
accepted terminal report wins while that lease is current. Normal terminal
acceptance already expires the lease, so it normally yields NONE; discovery
does not change terminal lifecycle behavior. Released/expired leases and
historical terminal reports are excluded. Multiple current slots or invalid
stored state produce a classified internal failure, not a truncated list.

No response contains a lease capability or digest, execution recipe, target,
object key, signed delivery material or artifact bytes. Neither state grants
renewal, release, upload, execution, deletion or acquisition authority.

## Serialization and withdrawal

All native v2 presence replacements and lease mutations take a bounded
transaction-scoped worker identity advisory lock before existing presence,
Run and lease row locks. This also serializes an identity with no presence row.
Discovery takes that same identity/presence boundary, then reads a fresh single
statement snapshot with `statement_timestamp()`. Waiting never makes an old
transaction-start clock or pre-wait snapshot authoritative. It changes no Run,
presence, lease, grant, artifact or terminal record.

Acquisition rechecks current presence, exact observed timestamp and availability
under the boundary before writing. A withdrawal acknowledged after commit
fences acquisitions based on an older AVAILABLE report. An acquisition committed
before withdrawal remains visible until released or expired; withdrawal is not
revocation. Another AVAILABLE report can invalidate a recovery observation.
External plan resolution and object verification remain outside transactions.

## Rollout and recovery

Deploy this binary to every control-plane writer before enabling discovery-based
manual recovery. Older binaries participate in presence-row locks but not the
new identity lock for absent presence. Do not claim the stronger first-report
serialization during a mixed-version rollout. No migration, new table,
extension or runtime privilege is needed. Rollback stops discovery clients and
retains all records; never rewrite migration history.

Recovery is still an explicit operator workflow, not a service restart policy:

1. Stop the old agent and its browser tree; ensure no other installation uses
   the worker identity.
2. Report UNAVAILABLE and wait for server acknowledgement. A local timeout,
   process exit or sleep is not a server fence.
3. Discover the current slot. If ACTIVE, retain local evidence and wait for
   authority expiry without trying to restore its lost capability.
4. Retain owner markers and files even for NONE. Resolve evidence disposition
   through the separately reviewed Playwright manual recovery procedure.

Shared credentials across independent installations cannot meet process
exclusion. This PR does not implement that worker procedure, automatic cleanup,
automatic retries, image publication or runtime promotion.

## Verification

Contract corpus and typed domain tests cover closed unions and timestamp
relationships. HTTP/application tests cover principal binding, paused
acquisition, no-store responses, input rejection and secret-safe failures.
Disposable PostgreSQL tests cover independent pools, unchanged durable records,
terminal precedence, expiry/release, ambiguous capacity, actual lock waits and
withdrawal fencing. Run `make validate` and `make db-validate` as described in
the [PostgreSQL guide](postgresql.md).
