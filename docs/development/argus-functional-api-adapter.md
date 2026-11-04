# Argus functional API pilot adapter

## TL;DR

- Run `make argus-adapter-check` after bootstrapping the repository.
- Run `go run ./tools/argus-adapter` from the repository root with one Argus
  adapter request on stdin.
- The pilot catalog lives in `../../.argus/test-catalog.json` relative to this guide.
- The checkout revision and the native test sources must match.

The adapter implements the Argus functional API request and result v1 protocol.
Its pilot covers run lifecycle, run authorization, baseline administration and
integration admission. Each catalog test maps to a literal top-level Go test in
`internal/httpapi`. The inventory check compares the catalog with the fixed
mapping and confirms the selectors are discoverable by Go.

Bootstrap dependencies with `make bootstrap`. The adapter uses the local Go
module cache and runs without a shell or package downloads. Pass the exact Git
SHA-1 checkout revision as `testRevision`. Display names are informational.
Both selected and full-suite requests execute exactly the IDs in their input
and return results in the same order.

A valid result exits zero even when a native test fails, times out or crashes.
Argus can interpret the per-test outcome. Malformed, foreign or stale requests
exit nonzero without result JSON. Native output stays within the process and
the adapter reports fixed failure codes. Keep execution commands and toolchain
selection under CI owner control.

## CI shadow pilot

The Argus functional API shadow workflow runs after a same-repository pull
request merges to main. It uses the trusted merge checkout for orchestration
and the adapter, the reviewed pull request head for native tests, and the
catalog at the pull request base. It replays the reviewed comparison into a
disposable Argus instance. The execution binding carries the pilot identity
and is copied with the exact reviewed head as its test revision for each run.

GitHub retains a run-scoped artifact for 14 days. The observation records the
pull request, base and head, delivery ID, manifest digest when available, and
the elapsed wall time of each attempt. A valid manifest yields a plan and
separate selected and full-suite attempt evidence. An empty selection records
an explicit empty status. A missing or rejected manifest or invalid normalized
full-suite attempt triggers a direct full pilot run with separately labeled
fallback results. Argus can conservatively select the whole pilot.

The shadow job is informational. The required validation workflow continues
to govern merges. Run `make argus-shadow-check` to exercise evidence
correlation and fallback locally, and `make argus-adapter-check` for the native
adapter contract.
