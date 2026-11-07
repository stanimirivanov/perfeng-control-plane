# Contract candidate admission

## TL;DR

- Run `make contract-admission` against the selected snapshot or pass a candidate directory.
- The gate uses the same named-root loader as runtime initialization.
- A receipt establishes loader compatibility for retained Run/native fixtures only.
- Full C6 adoption still requires reviewed source pins, source-byte provenance receipts and serving conformance.

## Commands

From the repository root, use the pinned Go toolchain and existing dependencies.
The command installs nothing and resolves no remote schema references.

```sh
make contract-admission
make contract-admission CONTRACT_ADMISSION_ARGS='--snapshot=/absolute/path/to/candidate'
go test -vet=off -count=1 ./internal/contract
make verify
```

The candidate directory contains `openapi.json` and
`api/native-worker/v2/openapi.json` at the snapshot's established paths.
The command confines reads to the opened directory, freezes the two files in
memory, then emits their SHA-256 identities after validation. It writes no
candidate or selected snapshot files.

## What is checked

Runtime initialization and candidate admission call the same API-family loader.
The current hard-coded Run and Native Worker roots must resolve, referenced
schemas must be supported, and patterns must compile. The gate replays the
retained embedded create/baseline/presence/lease/terminal examples and verifies
that an extra field is refused in each tested closed payload.

A deliberately renamed root, missing/external reference, malformed pattern,
requiredness drift and permissive unknown-field policy must fail the regression
suite. Candidate rejection cannot change the currently installed runtime schemas.
The receipt scope is `run-native-retained-fixtures`, not general schema equivalence.
It does not cover Integration, catalogue/analysis payload schemas or every possible
Run/native payload. Those boundaries retain their existing validation suites.

## C6 rollout boundary

This gate is the C6a prerequisite. C2–C4 source split work must finish review before
new snapshot bytes are selected. Full C6 must compare every selected byte with its
immutable source Git blob/archive, preserve independently pinned family ledgers,
record any path transformation, and run existing API/serving conformance against
both old and candidate pins. No source attestation is inferred from a locally
self-consistent checksum ledger. The console's three supplemental files remain
owned by its consumer admission receipt.

No contract pins are updated by C6a. The independently failing C5b Effect generator
qualification does not authorize changing schema meaning or replacing validators.

The confined filesystem follows the pinned standard-library APIs
[os.OpenRoot](https://pkg.go.dev/os#OpenRoot) and
[Root.FS](https://pkg.go.dev/os#Root.FS). Root confines child symlink resolution;
opening the explicit root directory itself follows its path.
