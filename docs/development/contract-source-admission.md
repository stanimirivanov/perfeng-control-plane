# Contract source admission

## TL;DR

- Run the explicit source gate before selecting new contract snapshot bytes.
- The gate compares every declared file to its immutable local Git source blob.
- The copied provider ledger determines independently pinned family overrides.
- Routine offline verification remains self-contained and never acquires sibling repositories.

## Command and prerequisites

Use the repository Go toolchain and an existing Git executable. The declared
source repository must already contain every selected full commit object.
Acquire source objects separately through the reviewed developer workflow.
The command never fetches objects or schemas and does not install dependencies.

```sh
make contract-source-admission CONTRACT_SOURCE=/absolute/path/to/perfeng-contracts
make contract-source-admission CONTRACT_SOURCE=/absolute/path/to/perfeng-contracts CONTRACT_SOURCE_ADMISSION_ARGS='--snapshot=/absolute/path/to/candidate'
```

The tool emits a JSON receipt only after every selected file matches. Retain
stdout as the admission receipt when reviewing source selection. The receipt
contains the exact ledger SHA-256 and a sorted inventory of snapshot path,
SHA-256, source commit and source path. It contains no source payloads or Git
credentials. The command writes neither source nor snapshot files.

## Provenance interpretation

Run API `openapi.json`, `transitions.json` and root example paths map to the
published `api/run-management/v1/` tree at the primary source commit. Policy
schemas/examples keep their source paths. Native Worker paths use their own
family commit. Catalogue, integration and analysis-publication files use their
explicit file lists and independently pinned commits from the original ledger.

A path with no reviewed mapping, ambiguous family override, malformed full
commit/digest, missing object, undeclared file, symlink, checksum mismatch or
source byte mismatch fails admission. A coordinated copied-file and checksum
edit still fails when its claimed immutable source is unchanged. Future source
families or path transforms need a focused mapping change and negative tests.

Git reads use fixed arguments, no shell interpolation and a bounded timeout.
Replacement objects, inherited Git override variables, lazy object fetching and
network transports are disabled. The provided object identity must resolve to a
commit. The candidate root uses the confined filesystem documented in
[candidate admission](contract-candidate-admission.md).

## Trust and rollout limits

This is the C6b source-binding prerequisite. It proves agreement with reviewed
Git content identity, not a signature, public repository ownership, release
approval or schema-language equivalence. Selecting a different source commit is
still an explicit review action. A successful source receipt and the C6a loader
receipt qualify different properties; both are needed before full C6 adoption.
Existing API/serving fixtures must also pass against the candidate and selected
pins. C2–C4 source artifacts must complete review before their bytes are adopted.

The existing selected ledger was checked against all 74 immutable source files
without altering a selected pin. No new snapshot is selected by this command.
The console owns attestation for its supplemental report schema/examples.
