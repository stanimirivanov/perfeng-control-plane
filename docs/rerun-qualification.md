# Private rerun adapter qualification

## TL;DR

- M05-PR59j joins real PostgreSQL, registry, collector and retained-report adapters.
- Evidence bytes are explicitly synthetic; lifecycle advances use trusted test setup.
- Run the disposable PostgreSQL gate; this is not a live or public end-to-end receipt.
- Issuer/trust selection, public admission and live execution remain separate gates.

## Scope and evidence

`internal/postgres/rerun_qualification_test.go` uses the existing accepted source,
service grants, immutable acceptance and sealed Job fixtures. Its source registry
entry lists only the original owner, never the synthetic child owner. The new
fixture composes real rerun-aware registry approval, evidence-version selection,
raw/normalized collectors, normalization planning and retained report planning
against actual PostgreSQL. Object publication/reads use a deterministic in-memory
fixture that checks complete references, byte counts and SHA-256 values.

The positive test opens new database handles between stages and asserts:

- Raw evidence retains the child Run identity and exact registered source references.
- Normalization planning/output approval use the retained source owner.
- Request, contracts version, original policy bytes, baseline artifact/snapshot and
  report producer survive post-PROVISIONING baseline retirement, rerun-grant
  withdrawal and a changed current report runtime entry.
- Report planning binds the actual registered child normalized candidate and never
  delegates a known child to current report/schema selection.

Negative scenarios require rejection of tampered raw source bytes, changed current
raw producer metadata and normalized output from a no-longer-approved normalizer
version. Expectations precede fixture execution; no approval/byte checks are
removed to qualify the fixture.

## Exact gate

Use an authorized disposable loopback PostgreSQL 17.11 instance and Go 1.26.11,
following [the database guide](postgresql.md). Set `PERFENG_TEST_DATABASE_URL` to
that disposable database, then run:

```sh
go test -vet=off -race -count=1 -v ./internal/postgres -run 'TestRerunEvidence(Chain|Qualification)'
make db-validate
```

The tests join the existing database gate; there is no new opt-in success flag.
Without the database environment ordinary Go tests skip live database coverage,
which is not a passing qualification receipt. General regression verification is
`make validate`. Tests create isolated databases using the existing fixture and
must never target production.

## Proof limits and remaining gates

These tests use synthetic manifests/results. They do not run a SUT, k6,
normalizer CLI, report evaluator, Kubernetes Jobs or object-store transfer. The
positive case stops at report planning, not report production or numerical
baseline comparison. Reopening handles is not process termination, PostgreSQL
restart or a distributed crash/recovery proof; existing crash tests remain separate.
Lifecycle transitions and registration are trusted test calls, not public requests
or evidence that an executor performed those transitions.

Current raw/normalizer producer review remains required; this qualification does
not create historical producer/profile snapshots or prove continuity after arbitrary
registry changes. Public rerun serving remains disabled. PR58 explicitly requires
issuer/trust selection before its verifier work; PR45/48/51 depend on that boundary.
Full live rerun acceptance still needs real execution and verified report receipts.
Do not mark M05-PR59 or the Maya journey complete from this fixture alone.

No production behavior, schema, dependency, deployment, flag or credential model
changes. Rollback removes these tests/docs without touching retained evidence.
