# Immutable rerun intent persistence

## TL;DR

- M05-PR50 stages atomic child, service lineage and immutable acceptance storage.
- No public route, rerun producer, public codec or execution activation is included.
- Actor/key replay is independent of legacy CreateRun and caller credential rotation.
- Current grants and exact baseline approval are fenced inside the write transaction.
- PR58/45 identity/authorization, PR59 execution and PR51 serving must qualify before use.

## Ownership and contract boundary

This implements the persistence foundation for [contracts PR49](https://github.com/stanimirivanov/perfeng-contracts/pull/116)
at merged commit `a1b9e554ab2b344d6481e6fec74d8a934821be21` (bundle 0.31.0,
Run API 0.8.0). `internal/runrerun` owns private intent/acceptance vocabulary;
`internal/postgres` owns transactional encoding and migration 0027. The embedded
public contract snapshot remains unchanged: private storage is not a public
RerunReceipt implementation or a completed rerun capability.

`AcceptRerun` is deliberately not called by serving/application composition.
Enabling a caller before PR59 consumes the stored selection would let ordinary
workers reselect resources and is unsupported. Authentication/delegation, complete
source-comparison admission, current catalogue/suite/environment/workload/policy
eligibility, artifact-byte/schema verification and access to every evidence owner
are prerequisites for the later inbound operation, not proved by this storage API.
Actor and caller inputs must come from independently verified identity, never a
browser payload or an unqualified BFF credential.

## Atomic acceptance and recovery

A transaction locks the actor issuer/ID/key namespace, then current source-service
`run:read` and `run:rerun` grant rows in permission order. Database time is read
after the locks. Revocation updates serialize against these shared locks; an old
read of a grant cannot authorize the write. Source service/creator/legacy owner
come from immutable reviewed `run_services`, not supplied service names.

Existing intent is replayed before current selection/approval checks. Source,
report UUID/digest, reason and actor kind must match; caller renewal preserves the
original caller. Exact expiry is a conflict, never a new child. Current rights
still apply, including read visibility on a different previously bound source
before returning a cross-source conflict. Legacy CreateRun semantics are unchanged.

New intent requires retained native report inputs and an exact registered source
analysis reference (`kind: normalized`, `analysis-result/v1`, JSON, 1 byte–1 MiB).
The retained policy/input digest is verified without network I/O. Each retained
baseline snapshot must still equal its APPROVED database record under shared row
locks, ordered by ID/version; no current/latest baseline is substituted. Registry
and object-store calls stay outside transactions. Publication-only sources without
native input capture are not inferred into this storage path; a separately reviewed
publication-backed adapter is required before admitting those sources.

Child Run, immutable inherited service binding, automatic binding audit and original
acceptance commit together. A failed receipt write rolls back all of them and
returns zero acceptance. The child's creator is the current actor; the acceptance
also preserves the source's original creator. `rerun:<sha256(actor identity)>` is
an internal native-owner namespace independent of the CI principal and caller;
reserve it from configured legacy identities when enabling service-mode admission.
It does not grant membership or allow the browser to select a native owner.

Receipts use a versioned private JSON envelope bounded to 64 KiB with a database
SHA-256 constraint. Full policy/approval inputs are read from immutable capture,
not duplicated into the receipt or projected as public JSON. Source/report/child
foreign keys and unique actor/key and child/receipt identities preserve association.
The SQL row is immutable: UPDATE, DELETE and TRUNCATE are refused. No retention
purger is introduced; keeping full receipts also keeps keys sealed. Any later
payload retirement must preserve tombstones through a reviewed migration.

`GetRerun` performs exact originating-actor/key/source recovery with a fresh read
grant, including after mutation permission is revoked. It does not dispatch work
or extend expiry. Unknown, foreign-actor, service-unbound and revoked selections
return the same storage not-found sentinel. Returned captured inputs remain private:
provider evidence authorization is required before public disclosure. Reopen and
process-exit tests recover the same original acceptance after an uncertain response.

## Migration, locks and rollback

Migration 0027 adds one empty table, constraints and guards only. It performs no
history scan/backfill, changes no existing rows and leaves old binaries compatible.
Normal schema/FK locks need the established bounded migration window; the new
primary/unique indexes cover actual lookup and child/receipt identities. The
source capture and service relations are prerequisites, not reconstructed data.

Apply through the explicit migration command using the trusted migration login.
Only the future qualified writer needs INSERT/SELECT on `rerun_intents` plus its
existing Run/service/baseline capabilities. Do not grant UPDATE/DELETE/TRUNCATE,
service-administrator privileges or schema ownership to runtime logins. Owners
and superusers remain trusted infrastructure, not an adversary these guards stop.
Write transactions request synchronous commit with existing 15-second operation
and 5-second lock bounds. Production fsync/backup/restore remain infrastructure
requirements. Row locks cover grants, sorted approvals and new child/service/FK
writes; no external calls occur while holding them. Work/disk grows per accepted
intent; no automatic audit retention policy is selected here.

Rollback application code while retaining migration, accepted children, service
lineage and receipts; repair forward. There is no destructive down migration,
activation flag or production deployment change. Future serving must wait for
identity, authorized comparison/byte reads and immutable execution fencing; it
must not expose this private object as the public contract body.

## Verification

Tests use a disposable loopback PostgreSQL database and pinned Go 1.26.11. They
cover empty/upgrade migration, no inferred history, concurrent duplicate acceptance,
distinct actor/key namespaces, changed intent/source conflicts, current revocation,
read-only recovery, exact baseline retirement behavior, report metadata limits,
SQL immutability, actual rollback after a receipt constraint failure, reopen and
separate process exit after commit without a response. Domain tests cover bounded
intent and the exact 24-hour replay deadline. Race checks supplement real database
serialization; they do not qualify future identity verification or dispatch behavior.

## Worker selection lookup: M05-PR59a

`GetRerunExecution` recovers the immutable acceptance by the server-owned child
native principal, child ID and exact request. It checks the child/source service
lineage and original actor against retained rows, then reuses receipt/input digest
and relationship validation. Returned inputs are separately decoded copies. It
cannot select by source ID, actor alone, a principal prefix or the newest intent.
An ordinary/unmapped Run returns not-found; no registry fallback is performed.

This private operation is for trusted workers holding an independently verified
claim. Its selectors are not credentials or lease proof, and it must never back a
browser/API read. Interactive recovery continues to use `GetRerun` and current
actor grants. Grant revocation and baseline retirement do not rewrite historical
policy/approval evidence; a recovered APPROVED snapshot does **not** authorize a
new dispatch. Current admission and lease fencing are still required at execution.

This is the first foundation slice, **not completion of M05-PR59**. Subsequent
work must connect dispatch and reporting resolvers to this exact child lookup,
preserve the accepted selection through normalization/reporting/collection and
prove that missing/corrupt associations cannot fall back to ordinary registry
selection. The original request alone does not establish that all runtime trust
adapters honor the accepted selection. No caller, flag, route or rerun execution
is enabled here; public serving remains blocked on the existing prerequisites.

## Frozen report planning: M05-PR59b

With `PERFENG_REPORT_COMPARISON_CAPTURE_ENABLED=true`, application composition now
wraps the ordinary retained planner with `RerunReportPlanResolver`. The same
instance supplies report Job generation, report-manifest/verdict approval and the
report collector's schema pin. The default-off configuration is unchanged. Custom
capture stores must implement the explicit rerun association and artifact-read
capability; otherwise composition fails rather than bypassing the new boundary.

`LookupRerunExecution` observes the child/owner association independently of request
metadata. Only an explicit ordinary result may use ordinary planning. A known
child with missing, corrupt or mismatched acceptance remains a failure; storage
outages and cancellation never become ordinary fallback. Association creation is
atomic with the child and the accepted rows are immutable, so legitimate ordinary
Runs cannot later acquire a rerun association between the classification and read.
There is no inference from a principal prefix, source name or current registry.

For a known rerun, the planner verifies its original acceptance/child request and
reads the exact registered child normalized-result artifact. It rejects metadata
substitution; it does not reuse the source candidate as the child result. Frozen
policy bytes, workload, report producer, schema pin, baseline artifacts and approval
snapshots are cloned from retained acceptance. Only the candidate is adapted to the
verified child artifact. A SHA-256 of that exact input document supplies the report
Job's existing capture annotation. Job generation and verdict attestation therefore
use the same selection and deterministic retry identity, without selecting a newly
approved baseline. Historical approval remains historical after retirement.

**BEHAVIOR-ADJACENT rollout:** apply existing migration 0027 before deploying this
binary with report capture enabled. Provision SELECT on `rerun_intents`; actual
rerun reads also require the existing source/child service, Run, artifact and
comparison-input read privileges. A missing table/privilege is unavailable, not
legacy mode. No new migration or privilege mutation is performed by the binary.
Roll back the application while retaining schema and acceptance, or keep report
capture disabled until its complete prerequisites are provisioned. Stored inputs
and older public contract pins remain unchanged.

PR59 is still incomplete. Dispatch/current-admission and raw/normalization trust
must use the immutable child binding before a public rerun producer is enabled.
This slice qualifies report planning/verification only; it does not prove full
change-to-execution-to-report acceptance or admit service identity/public routes.

## Sealed dispatch templates: M05-PR59c

Migration 0028 adds `rerun_templates`, an immutable child/owner-scoped record of
one independently reviewed reusable Kubernetes Job template. The existing
execution table retains only cluster UID and spec hash; neither those fields nor
current registry contents can reconstruct missing historical template bytes.
This is prospective child-template capture, not backfill of a source Job.

`RetainRerunTemplate` validates the existing reusable-Job policy, encodes a private
version-1 document bounded to 1 MiB, verifies the accepted child association and
serializes capture on the child Run row. First insertion requires actual state
VALIDATING, target KUBERNETES and no execution binding. SQL guards enforce those
conditions too. Equal replay precedes lifecycle checks, so later retry/cleanup
can recover the same template; a different proposal conflicts without replacement.
A caller-supplied stale state cannot authorize a first late capture.

`GetRerunTemplate` rechecks immutable child/request relationships, document shape,
size, digest and reusable-Job policy before returning an independently owned Job.
The private envelope SHA-256 is not the dispatcher's runtime spec hash. UPDATE,
DELETE and TRUNCATE are refused. Missing or corrupt data is not permission to
regenerate a template after dispatch may have happened. Capture/read use bounded
operation contexts and existing transaction/lock limits, with no Kubernetes or
registry/object-store I/O inside transactions.

These methods are trusted-worker storage capabilities, not an authorizer, lease
proof or dispatch permit. The future consuming adapter must establish current
selection eligibility, active lease ownership and reviewed template provenance,
then preserve the seal through validation, creation/adoption and cancellation.
No runtime caller or producer is added here. Native/benchmark execution needs its
own qualified consumer path; this storage primitive does not qualify either path.

Apply 0028 using the explicit trusted migration command before enabling a future
consumer. It adds an empty table/index/constraints/guards, uses normal schema/FK
locks, scans no old Runs and infers no history. Provision SELECT/INSERT only for
qualified consumers, never schema ownership or update/delete/truncate rights.
Rollback code while retaining schema, templates and acceptance; repair forward.
No down migration, dependency/snapshot change, flag enablement or deployment is
included. PR59 remains incomplete until dispatch/admission and raw/normalization
integration qualify. Sealing a template alone proves no end-to-end rerun flow.
