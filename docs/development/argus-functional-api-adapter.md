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
selection under CI owner control when composing the later shadow workflow.
