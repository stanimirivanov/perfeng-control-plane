# Report-time comparison input retention

## TL;DR

- `PERFENG_REPORT_COMPARISON_CAPTURE_ENABLED` is explicit and defaults to false.
- New enrolled Runs retain their exact report selection before Job dispatch.
- Job creation, schema selection and attestation reuse that selection on retry.
- Old Runs and existing publications are not backfilled or rewritten.
- This is PR47's capture prerequisite, not PR48's public comparison endpoint.

## Stored meaning and boundaries

Migration 0026 adds immutable admission-time enrollment and
`report_comparison_inputs`. The versioned private record preserves original
CreateRun request (candidate/suite/profile/catalog/environment/policy pins),
candidate normalized artifact, resolved workload, exact policy bytes, report
producer and contracts version, all policy baseline resolutions, and complete
approved baseline snapshots for non-null resolutions. A nil reference remains
unresolved even if that baseline is approved later. Approved snapshots preserve
record revision/lifecycle and exact normalized artifact; later retirement never
rewrites them. Their canonical baseline serialization can establish the existing
baseline content identity without consulting today's registry or approval state.

The core `runcomparison` model owns validation/cloning and the storage boundary;
PostgreSQL owns private serialization, digest, FKs, lock/replay and migration.
Registry/Kubernetes/object-store calls remain outside database transactions.
Core types are not a public response or a new analysis/verdict schema.
The approved comparison contract is
[contracts PR #112](https://github.com/stanimirivanov/perfeng-contracts/pull/112)
(candidate bundle 0.29.0, Run API 0.6.0); the embedded API snapshot stays unchanged.

## Capture, attestation and recovery

An opt-in repository enrolls a Run only inside its new native/Integration
admission transaction. Original idempotency replays and old-binary writes stay
unenrolled. The enrollment bit cannot be updated. Legacy writers omit the new
column, so default-off behavior remains compatible before schema expansion.

The shared retained resolver reads first. If present, it never resolves current
registry/approval again. For a first selection it obtains trusted policy/runtime
and baseline evidence, then commits before any report Job can be dispatched.
The transaction locks the native Run, checks exact request and registered
candidate, and the SQL guard rechecks selected baseline approval/snapshot under
shared row locks, ordered by baseline ID/version. No report already registered
for that Run can acquire a new input record. The digest identifies immutable
selection bytes, not execution success or a performance verdict.

The report Job's Pod-template annotation `perfeng.dev/report-inputs-sha256`
participates in the existing Job fingerprint. It hashes canonical input JSON;
the table checksum separately covers the versioned storage envelope. A legacy Job without that marker
cannot be adopted as captured execution. The output approver and report schema
selector use the same retained inputs. Object bytes, producer, policy/verdict
and existing lifecycle/lease/revision verification still apply; retention
never authorizes an artifact fetch or restores an execution lease.

Equal concurrent capture/replay returns the original record; changed inputs
conflict. Commit errors remain uncertain: retry through read-first resolution,
never choose a fresh baseline to get past a failure. Returned policy/producer/
baseline values are owned clones. A crash after selection commit but before
dispatch leaves the selection available for an identical retry.

Unenrolled work uses the existing reporting path without being marked captured.
Missing historical inputs remain missing, even when today's catalog could fill
in plausible values. Existing registered reports bypass Job execution as before.
Immutable request-linked publications and retained verification/native bindings
remain authoritative and are neither duplicated nor rewritten here. PR48 must
reuse a complete publication-backed association where available before treating
absence of a native input record as incomplete history. This storage read alone
is not an API-wide completeness decision.

## Activation, privileges and rollback

1. Apply migration 0026 with the existing explicit migration command and trusted
   migration login. It adds a fast-default false column and empty table/guards;
   no Run scan, rewrite or selection backfill occurs. Normal schema/FK locks
   require the established bounded maintenance window.
2. Grant only the trusted worker runtime SELECT and INSERT on
   `perfeng_control.report_comparison_inputs`; existing Run/artifact/baseline
   privileges continue to apply. Do not grant UPDATE/DELETE/TRUNCATE.
3. Stop old workers before enabling the flag consistently for admission and
   reporting. Mixed capture-enabled admission and legacy reporting workers are
   unsupported. Pure application composition checks the durable-store capability
   and uses the same plan boundary for Job generation and attestation.
4. Set `PERFENG_REPORT_COMPARISON_CAPTURE_ENABLED=true` for future Runs only.
   Invalid Boolean values fail configuration. No default activation or deployment
   mutation is included in this PR.

Writers request synchronous commit and use existing 15-second operation and
5-second lock bounds. SQL update/delete/truncate guards preserve accepted input
records; database owners/superusers remain trusted infrastructure. Roll back
application/flag while retaining schema and records; repair forward. To resume
captured in-flight Runs, re-enable capture-aware workers so they reuse their
original marker/plan rather than trying to adopt Jobs with a different template.
No destructive down migration or approval re-creation is supported.

## Verification and remaining gates

Pinned Go 1.26.11 tests cover uncertain-commit replay without registry reads,
attestation, schema pin and stable Job identity, unresolved-reference replay,
upgrade/no enrollment backfill, scope, concurrent capture, retirement/reopen,
changed-input conflict, direct SQL guards and pre-capture approval revocation.
Use `make validate` and the disposable PostgreSQL `make db-validate` command.
No production migration or cluster mutation is needed to verify capture logic.

Public service-authorized comparison serving, object/policy byte transfer,
projection-size enforcement and live Console acceptance belong to PR48 and its
prerequisites. Capturing selections is not proof of a deployed comparison API,
normalized-byte integrity or a verdict. See
[ADR-0035](decisions/0035-retain-report-time-comparison-inputs.md).
