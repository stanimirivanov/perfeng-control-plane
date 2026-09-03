# Contributing

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
