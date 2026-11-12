# Service ownership and grant persistence

## TL;DR

- Migration 0025 expands storage only; existing HTTP visibility stays principal-owned.
- Explicit reviewed Run bindings are immutable; no legacy backfill is inferred.
- Exact database logins administer grants only for owner-provisioned services.
- Grants and append-only audit commit together; every observation reads current state.
- Delegated identities, public service access and mutation fences remain later slices.

## Boundaries and rollout

This is M05-PR44, an M06 persistence prerequisite for the Console. It follows
[contracts PR #110](https://github.com/stanimirivanov/perfeng-contracts/pull/110)
and its service identity contract at merged commit
`4ad2582` (candidate bundle 0.28.0, Run API 0.5.0). The embedded public API
snapshot is unchanged: no new HTTP endpoint or service-mode configuration is
activated. Later provider slices must admit their reviewed snapshot before use.

`run_services` preserves exact service issuer/ID, CI or human creator issuer/ID
and kind, original native principal, and the reviewed binding reference. The
composite native FK prevents associating another principal's Run. Updates,
deletions and truncation are refused. No current catalog lookup, display name,
principal spelling or client claim can establish a binding. An unmapped Run
remains legacy-owner-only. Baseline enforcement must use an explicitly reviewed
source-Run association or a later reviewed baseline binding; this PR does not
activate baseline service access.

`service_grants` keys each independent right by exact service and actor issuer/ID.
Revocation retains an `enabled=false` tombstone. `GetServiceRunBinding` returns
not-found for absent/unbound Runs, missing grants and revoked access without
owner fallback. `HasServicePermission` reads exactly one right. Neither caches
nor implies other rights. Their actor input must come from future independently
verified authentication; the storage functions do not verify tokens/delegation.
These observations are not authorization fences for a later mutation. That
mutation must recheck/lock current grants within its own transaction.

## Initial operator administration

Use a separately authenticated PostgreSQL administrator login over verified TLS
outside disposable loopback tests. Provision logins in infrastructure; this
migration creates none. Never grant runtime logins ownership, schema CREATE,
admin-role membership or direct grant/audit/administrator-table writes. Schema
owners and superusers are trusted infrastructure and can override database
protections; this is not a defense against a compromised DBA.

The schema owner explicitly assigns the administrative login to one service:

```sql
INSERT INTO perfeng_control.service_administrators
    VALUES ('urn:services', 'search', 'service_operator');
GRANT USAGE ON SCHEMA perfeng_control TO service_operator;
GRANT EXECUTE ON FUNCTION
    perfeng_control.set_service_grant(text,text,text,text,text,boolean,text)
    TO service_operator;
```

In that login's own authenticated session, grant or revoke one right:

```sql
BEGIN;
SET LOCAL statement_timeout = '15s';
SET LOCAL lock_timeout = '5s';
SELECT perfeng_control.set_service_grant(
    'urn:services', 'search', 'urn:people', 'maya', 'run:read', true, 'review/330');
COMMIT;
```

Use `false` to revoke. The function checks the actual `session_user` against
the service assignment and holds that row FOR SHARE until transaction end; no actor/administrator argument confers authority.
Its definer has a fixed safe search path, qualified references, bounded lock
wait and no PUBLIC execution. Operators have function EXECUTE, not table DML.
Only the schema owner may bootstrap or remove administrative assignments.

For historical binding import, the schema owner first verifies retained admission
and the original creator against an immutable reviewed source. In one bounded
transaction, INSERT into `run_services` by selecting `run_id, principal` from
the exact native Run, supplying the reviewed service/creator/kind and `review_ref`.
Require one returned row and inspect its BIND_RUN audit. This is an explicit
trusted import, not an admission shortcut or a claim that review references
are cryptographically verified by PostgreSQL. No bulk import runs at migration.

Only grant these new read privileges to a future reviewed service-mode adapter:

```sql
GRANT SELECT ON perfeng_control.run_services, perfeng_control.service_grants
    TO perfeng_runtime;
```

## Transactions, audit and recovery

Bindings and changed grant states trigger `service_access_audit` in the same
transaction, recording database time, actual session login, exact service/actor,
Run or permission, operation and review reference. Update/delete/truncate of
retained audit is refused. Audit writes force synchronous commit for local WAL
flush; infrastructure still owns fsync/storage durability and backup/restore.
Lock order is administrator assignment, then the exact grant key; removing an
administrator waits for an in-flight change to finish. Concurrent identical grants serialize on the exact key and produce one state
change/audit. Same-state replays preserve the original review reference. Opposite
changes serialize with last committed state; there is no automatic retry.
A lost response has an uncertain commit outcome: the owner inspects current
state/audit before deciding whether a new administrative change is intended.
Blindly repeating an old grant could override an intervening legitimate revoke.

Migration 0025 creates empty domains/tables/indexes/functions/triggers and takes
normal schema/catalog and referenced-table constraint locks. It neither scans
nor rewrites/backfills Run data. Use the existing bounded migration window.
Old binaries ignore expanded storage. Roll back application code while retaining
bindings, grants, administrators and audit; repair forward, never rewrite applied
migration checksums or delete history to force a retry.

## Verification

`make verify`, `make race`, and `make db-validate` exercise domain input checks,
empty/populated upgrade, immutable SQL guards, independent logins and services,
atomic rollback, concurrent replay, exact issuer scope, revocation and reopen.
The disposable database follows [PostgreSQL testing](postgresql.md#isolated-integration-tests)
and the pinned CI loopback-trust service; its admin additionally needs CREATEROLE
for isolated test logins, cleaned up with their disposable database. No production
login/database is modified. Live user authentication, delegated client authority,
HTTP denial and audited rerun remain later acceptance gates.
