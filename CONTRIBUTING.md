# Contributing to perfeng-control-plane

## TL;DR

- Deliver one coherent, verified control-plane outcome per issue and pull
  request.
- Use MUST, SHOULD and MAY as defined below; lowercase wording is explanatory.
- Proceed on documented, reversible assumptions and stop for decisions that
  materially affect behavior, compatibility, persistence, security or external
  state.
- Prepare requested changes without staging or committing, then print issue
  content for the repository owner.
- Keep lint findings informational; never add suppression comments merely to
  make the report quiet.
- Run the exact repository checks and report unavailable checks as not run.
- Use exact published milestone titles in the form `MNN — Outcome`.

## Policy language and sources of truth

- **MUST** and **MUST NOT** identify requirements. A deviation needs an explicit
  reviewer-approved exception recorded in the issue or pull request.
- **SHOULD** and **SHOULD NOT** identify strong defaults. A deviation is allowed
  only when its reason and trade-off are recorded.
- **MAY** identifies an optional choice.

Lowercase words such as “prefer,” “avoid” and “may” provide guidance and do not
silently create another normative level.

| Concern | Canonical source |
|:--|:--|
| Contributor workflow, issue timing, escalation and completion | This document |
| Concise contributor and agent entry point | [AGENTS.md](AGENTS.md) |
| Architecture and Go engineering | [docs/development/engineering-standards.md](docs/development/engineering-standards.md) and repository-specific rules below |
| PostgreSQL schema evolution | [docs/development/sql-migrations.md](docs/development/sql-migrations.md) |
| Architecture decisions | [docs/decisions](docs/decisions/README.md) |
| Cross-repository milestones | [perfeng-environment roadmap](https://github.com/stanimirivanov/perfeng-environment/blob/main/docs/roadmap/milestones.md) |
| Current capabilities and exact commands | [README.md](README.md) and checked-in tool configuration |

When sources conflict, the narrower source governs its stated concern. An
accepted ADR governs its decision until superseded. A contributor MUST surface
an unresolved conflict rather than quietly choosing the convenient rule.

## Before starting

A contributor MUST inspect the branch and working tree and preserve unrelated
changes; read affected code, tests, operational guides, standards and ADRs;
identify the smallest independently reviewable outcome; identify affected APIs,
contracts, migrations, security boundaries, concurrency and operations; and
select the workflow under [Issue timing](#issue-timing).

A contributor MUST NOT discard, overwrite, reformat or incorporate unrelated
work merely to obtain a clean diff. Destructive operations and externally
visible writes outside the stated task require explicit authorization.

An ADR MUST be proposed when a change establishes or revises a durable decision
about compatibility, persistence or data meaning, security, deployment
topology, foundational technology, ownership or cross-component behavior.

## Ambiguity and escalation

Before escalating, a contributor SHOULD inspect relevant code, tests, fixtures,
documentation, ADRs and issue history and perform safe read-only investigation.

A contributor MAY proceed with a documented assumption only when the choice is
within scope, local, reversible and inexpensive; does not alter public contracts,
persisted meaning, security, external state or destructive behavior; does not
weaken acceptance or verification; and would not materially change what
reviewers believe they are approving. Record the assumption and consequence.

A contributor MUST stop and request a decision when missing information could
materially change the outcome, milestone, compatibility, data meaning or loss,
security, authorization, an irreversible action, external state, operational
ownership, deployment topology or a choice between materially different
designs. State the exact decision, evidence checked, options and consequences,
and work that can safely continue.

## Issue timing

- When an issue number is supplied, the contributor MUST follow its scope and
  milestone. A material mismatch follows the escalation rules.
- When no issue number is supplied, prepare the smallest coherent requested
  change and print a complete proposed issue in the completion report. The
  owner creates the issue and then creates or renames the branch so it contains
  the issue number.
- Contributors acting for the owner MUST NOT stage, commit, push, create or
  modify issues, create pull requests, publish images or releases, run database
  migrations, or change milestones unless explicitly requested.
- Read-only investigation and scoping MAY occur before an issue exists.

Creating or modifying a GitHub record, image, release, cluster or database is an
externally visible write and requires explicit authority.

## Milestones and required issue

Use exact published milestone titles in the form `MNN — Short outcome`.
Published milestones MUST NOT be reused, renumbered or casually renamed. GitHub
is authoritative for live assignment; the centralized
[roadmap](https://github.com/stanimirivanov/perfeng-environment/blob/main/docs/roadmap/milestones.md)
owns intended outcomes and sequencing.

Every implementation issue uses:

~~~markdown
**Repository:** perfeng-control-plane
**Milestone:** MNN — Outcome

## Goal

Describe the problem and observable result.

## Scope

- Included behavior and boundaries.

## Design decisions

- Choices, assumptions, compatibility, concurrency, persistence and ADRs.

## Security and operations

- Trust boundaries, credentials, external effects, rollout and recovery.

## Acceptance criteria

- [ ] Observable behavior and verification evidence.
- [ ] Relevant rejection, concurrency and failure behavior.
- [ ] Documentation, migration and operational effects.

## Verification

- Required, focused integration, race and live checks.

## Out of scope

- Explicit exclusions and deferred work.
~~~

Acceptance criteria MUST describe observable behavior or verifiable invariants,
not implementation activities. Known limitations MUST be visible.

## Pull-request-sized work

A pull request MUST solve one coherent problem, leave the repository buildable,
include implementation, tests, documentation and migrations required for that
outcome, and contain evidence for expected and important failure behavior.

Split independent behavior, broad cleanup, dependency upgrades, schema redesign
and unrelated formatting. A thin vertical slice may legitimately touch domain,
application, adapters, persistence and tests. Placeholder abstractions and
future configuration MUST NOT be added merely to make a roadmap appear complete.

## Development workflow

1. Select the workflow defined in [Issue timing](#issue-timing).
2. Prepare one focused change without staging, committing or pushing for the
   owner.
3. Add or update tests with the behavior.
4. Implement the smallest coherent solution and update documentation.
5. Format and run the exact repository checks.
6. Review the diff for secrets, generated churn, contract changes, assumptions,
   migration safety and unrelated edits.
7. Print the canonical [completion report](#completion-report).
8. The owner creates or confirms the issue and branch, may adjust the change,
   commits with `[#N]`, pushes and opens the pull request.

Do not store task-specific issue bodies, PR descriptions or handoff notes in
the repository.

## External operations and destructive actions

Read-only inspection is allowed when relevant. Filesystem, database, cluster,
registry, GitHub and other mutations MUST remain within the requested scope and
granted authority.

- Tests MUST use disposable PostgreSQL and Kubernetes resources, never
  production systems.
- Database migrations, image publication, releases and cluster mutation require
  explicit execution paths and authorization.
- Credentials MUST NOT appear in arguments, logs, fixtures or Git.
- Data deletion, schema contraction, credential rotation, resource replacement,
  pruning and history changes require explicit authorization and exact targets.
- A partial operation MUST retain enough non-secret evidence for diagnosis and
  safe retry.

## Toolchain and local checks

Use the Go version in go.mod (currently 1.26.6). With Go's default
`GOTOOLCHAIN=auto`, an older Go installation downloads the required toolchain.
With `GOTOOLCHAIN=local`, install that version yourself. Race tests also need
a supported C compiler; CI uses Linux.

The linter version is pinned in [.golangci-lint-version](.golangci-lint-version).
Use the official release binary; do not let the editor install `@latest`.
The linter bundles gofmt/goimports, so no separate formatter installation is
needed for the acceptance checks. For Windows x64, from this repository:

~~~powershell
$lintVersion = (Get-Content .golangci-lint-version -Raw).Trim()
$lintArchive = "golangci-lint-$lintVersion-windows-amd64.zip"
$lintDirectory = Join-Path $PWD ".local/quality-tools"
New-Item -ItemType Directory -Force $lintDirectory | Out-Null
Invoke-WebRequest "https://github.com/golangci/golangci-lint/releases/download/v$lintVersion/$lintArchive" -OutFile "$lintDirectory/$lintArchive"
$lintHash = (Get-FileHash "$lintDirectory/$lintArchive" -Algorithm SHA256).Hash
if ($lintHash -ne "4735fdc8e84a0cfb7a15a1c364a650942f88215e0d36c674ebc4024f7b554524") {
  throw "Linter checksum mismatch; do not execute the download"
}
Expand-Archive "$lintDirectory/$lintArchive" -DestinationPath $lintDirectory -Force
$env:PATH = "$lintDirectory/golangci-lint-$lintVersion-windows-amd64;$env:PATH"
golangci-lint version
~~~

This changes PATH for the current shell only. For VS Code, make that extracted
directory available on your user PATH and fully restart VS Code, or set the
Go extension's `go.alternateTools` entry for `golangci-lint` to the binary's
absolute path in your user settings. Do not commit machine-specific paths.
Other platforms should download the matching archive from the same
[official release](https://github.com/golangci/golangci-lint/releases/tag/v2.13.2)
and verify its published checksum before placing the binary on PATH.

From the repository root, run each command and check its exit status before
continuing (PowerShell does not stop automatically after a native command fails):

~~~sh
go version
golangci-lint version
golangci-lint config verify
golangci-lint run ./...
gofmt -l .
go mod verify
go test -vet=off -race -count=1 ./...
go test -vet=off ./internal/httpapi -run=^$ -fuzz=FuzzParseJSON -fuzztime=10s
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
~~~

`golangci-lint run` reports findings without changing files or failing on
warnings. Read the report; a zero exit status does not mean there are no findings.
The separate `gofmt -l .` check must print nothing. Use
`golangci-lint fmt` to apply gofmt and goimports, then review the diff. Do not
apply lint autofixes over the whole repository as part of unrelated changes.
VS Code uses gopls formatting/import organization on save and the same
informational lint configuration as CI for package diagnostics. Keep the Go
extension current enough to support
`golangci-lint-v2`. Automatic tool updates are disabled.

Tests use `-vet=off` because vet already runs in the informational lint
report; the implicit vet pass in `go test` must not turn those findings into
a second acceptance gate. Compilation errors, test failures and detected races
still fail the test command.

Ordinary tests skip live PostgreSQL tests without `PERFENG_TEST_DATABASE_URL`.
Follow [the isolated database instructions](docs/postgresql.md) for storage
changes. CI runs these tests against a disposable pinned PostgreSQL service,
including restart and concurrency checks. Never use a production database.

## Verification and constrained environments

A contributor MUST run every applicable required check supported by the current
environment. `make validate` wraps the non-live formatting, lint report, module,
race, fuzz and vulnerability checks. Focused PostgreSQL, Kubernetes and image
checks remain required when the changed boundary needs them.

Narrower checks MAY provide interim feedback but MUST NOT be presented as the
complete suite. When a required check cannot execute because of platform,
sandbox, network, credentials, missing compiler, unavailable disposable service
or another environmental constraint, the contributor MUST:

1. report the exact command as **not run**;
2. state the blocker and safe attempts made;
3. list checks that did run;
4. state residual risk and where the missing check should run; and
5. avoid weakening the check merely to make the environment pass.

An unexecuted check MUST NOT be reported as passed. A failure MUST be reported
even when it appears unrelated; identify a verified pre-existing failure
separately with evidence. Informational lint findings are not failures, while a
broken linter invocation or configuration is a failure.

## Automated policy

The informational configuration explicitly enables vet, Staticcheck (excluding
optional QF quick fixes), unused/ineffective assignment checks, error checking,
wrapped-error comparisons, SQL row lifecycle checks, selected revive rules,
documentation checks, whitespace suggestions and cognitive complexity above 20.
Tests are checked too; no blanket test exclusions or new-code-only baseline
hides existing findings.

All lint findings are review information. The repository owner decides which
findings warrant changes; a warning alone is not a reason to rewrite code.
There is no zero-warning target or mandatory complexity/function-size limit.
Do not add lint-suppression comments, redundant explanations or artificial
helpers to satisfy tools. Leave declined suggestions visible in the report.

Lint findings have exit status zero. Configuration or tool failures must still
be investigated; do not hide a broken report with `continue-on-error` or
`|| true`. Formatting, compilation, test execution, module verification and
vulnerability scanning remain separate acceptance gates.

The vulnerability scanner is pinned at v1.7.0 in the workflow and commands,
runs against the application module without adding tool dependencies to go.mod,
and queries the current Go vulnerability database. Reachable findings fail CI;
a clean scan is not proof of absence of vulnerabilities. Fix confirmed findings
with reviewed dependency/toolchain updates instead of hiding them.

When upgrading tools, update the linter version file and download checksum,
or the scanner version in both CI and these commands (also README), together.
Review changed diagnostics, run the lint report and the full test suite.
The Go patch and x/text versions were raised when introducing these checks to
address the initial [standard-library](https://pkg.go.dev/vuln/GO-2026-6090) and
[x/text](https://pkg.go.dev/vuln/GO-2026-5970) findings.

## Go house style

- Separate logical phases with blank lines: validation, bounded context,
  transaction setup, reads, decisions, writes and commit. Keep an operation
  adjacent to its error check. Avoid blank lines between every statement.
- Use multiline calls, SQL and nested composite literals when they are hard
  to scan. Roughly 120 columns is a readability target, not an absolute limit;
  URLs and small data tables may reasonably exceed it.
- Functions should have a coherent responsibility and one main abstraction
  level. Prefer early returns and shallow nesting. Extract helpers for concepts
  such as reading claim candidates, not merely to hit a line-count quota.
- Name effects explicitly: a helper that acquires a lock should make that
  visible in its name or contract. Short receiver names and local loop indices
  are fine; avoid ambiguous names for ownership, state or time.
- Write comments about intent, invariants and contracts, not narrations of
  syntax. Exported declarations and interface methods need comments starting
  with their name. Document package purpose.
- Put implementation-independent behavior on interfaces: inputs, success and
  empty results, relevant sentinel errors, concurrency guarantees and ownership.
  Concrete methods describe adapter-specific details without duplicating the
  whole contract. Private helpers document locks/resources they require or own.
- State assumptions explicitly: terminal-state handling, cancellation priority,
  revision checks, idempotency, lease validity and clock source. A linter cannot
  verify these contracts for us.

## Errors, resources and concurrency

Handle errors when they can affect the operation's outcome, including test
setup and cleanup failures. Use `errors.Is` and `errors.As` for classified
errors. Wrap errors only when it preserves an
intentional public contract and does not expose driver details or secrets.
Never put DSNs, bearer credentials, lease tokens or raw database row data into
HTTP responses or ordinary diagnostic logs.

Acquire resources with explicit ownership and bounded contexts. Usually defer
cleanup immediately after successful acquisition. SQL rows must be checked for
iteration errors and closed before another statement on the same transaction;
an explicit early close can be required. Commit remains explicit and its error
must be returned. Do not add a generic transaction wrapper simply to hide
begin/rollback/commit.

Deferred transaction rollback is a cleanup convention, not another result of
the operation: after commit it normally returns `sql.ErrTxDone`, and after a
failed statement it must not replace the primary error. Keep this rationale
here rather than repeating it beside every `defer tx.Rollback()`.

Keep short database transactions free of external network/service calls.
Preserve run-row lock order, the fresh post-lock eligibility statement and
per-candidate database time. Moving the clock read outside a claim loop, changing
query boundaries or broadening lock scope is a behavioral change requiring
concurrency review and regression tests, not a formatting improvement.

Keep source comments for useful contracts and non-obvious, local invariants.
Shared conventions belong in this guide or package documentation. Do not add
comments that merely restate standard library behavior or silence a linter.

## Documentation

Every long narrative document MUST contain a `## TL;DR` immediately after its
title and status metadata. A document is long when it has at least 800 words,
more than five second-level sections, or is an architecture, security,
compatibility, migration or operational guide readers consult selectively.
Issue and pull-request templates are excluded.

The TL;DR SHOULD state purpose, intended reader, important outcome and next
action in three to seven bullets. Public APIs, persistence, configuration,
migrations, operational behavior and troubleshooting guidance change with the
code that makes them inaccurate. Accepted ADRs are historical records;
supersede rather than rewrite them.

## Pull request description

A pull request MUST state the linked issue and milestone; problem, outcome and
exclusions; assumptions and unresolved questions; API, contract, persistence,
concurrency and security effects; verification including checks not run;
migration, rollout, rollback and recovery; and known limitations.

## Review checklist

- Does the code read in logical phases with clear names and manageable nesting?
- Are exported/interface contracts and non-obvious helper invariants documented?
- Are errors checked, classified safely and tested on meaningful failure paths?
- Are resource ownership, cancellation and transaction/lock boundaries explicit?
- Are state transitions, idempotency and concurrency guarantees unchanged or
  deliberately tested when changed?
- Do tests assert setup errors, behavior and invariants rather than only coverage?
  Use focused cases, `t.Helper` for helpers and deterministic synchronization
  where possible; avoid sleep-based concurrency assertions.
- Do formatting, race tests and vulnerability scanning pass? Were lint findings
  considered on their merits and live storage tests actually run when needed?
- Are migrations and pinned contract snapshots untouched unless their change
  is explicitly part of the work? Are generated binaries and local secrets absent?

Further guidance:
[Go code review comments](https://go.dev/wiki/CodeReviewComments),
[Go documentation comments](https://go.dev/doc/comment),
[golangci-lint configuration](https://golangci-lint.run/docs/configuration/file/),
[Go vulnerability management](https://go.dev/doc/security/vuln/).

## Completion report

After every work item, print the repository, exact milestone, proposed issue
title, complete issue body, assumptions and limitations, verification outcomes
distinguished as passed/failed/not run, and a suggested `[#N]` commit subject.
When an issue exists, repeat or amend its content so delivered scope is auditable.
Printing the report does not publish external records.
