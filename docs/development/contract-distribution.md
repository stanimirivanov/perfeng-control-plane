# Preparing and restoring contract snapshots

## TL;DR

- Use pinned signed distribution artifacts to prepare snapshot updates.
- Stage candidates in a fresh directory and run loader and source admission.
- Retain the previous snapshot and signed inputs for offline restoration.

Stage the exact tool and historical release inputs described in the
[shadow guide](contract-distribution-shadow.md). Use an operator-controlled
parent for the new candidate directory.

```sh
make contracts-update-check CONTRACTS_SHADOW_ASSETS="$assets" \
  CONTRACTS_BOOTSTRAP="$bootstrap" CONTRACTS_VERIFIER="$verifier" \
  CONTRACTS_STAGING_TARGET="$scratch/candidate"
make contract-admission CONTRACT_ADMISSION_ARGS="--snapshot=$scratch/candidate"
make contract-source-admission CONTRACT_SOURCE="$contracts_source" \
  CONTRACT_SOURCE_ADMISSION_ARGS="--snapshot=$scratch/candidate"
```

Preparation authenticates all five independent families and reconstructs their
selected files through the signed materializer. Only after every family
succeeds does it create the candidate directory. Review its diff against
`internal/contract/snapshot` and use the candidate in the reviewed checkout.
Changes to selected contract revisions require a separate compatibility review.

Before cutover, retain the clean previous snapshot, its consumer commit and
the exact signed assets with tool, verifier and trust inputs. Recreate a fresh
candidate and check it against that retained snapshot:

```sh
make contracts-restore-check CONTRACTS_SHADOW_ASSETS="$retained_assets" \
  CONTRACTS_BOOTSTRAP="$bootstrap" CONTRACTS_VERIFIER="$verifier" \
  CONTRACTS_STAGING_TARGET="$scratch/restored" CONTRACTS_ROLLBACK_SNAPSHOT="$previous_snapshot"
```

The retained snapshot must have exactly the selected files and ledger.
Substitution, missing files, extra files and symbolic links fail. This
rehearsal covers distribution recovery at the same selected family revisions.
It requires the preprovisioned bootstrap and Go dependencies to be available.

The preparation receipt embeds the authenticated selection receipt and its
consumer commit, snapshot ledger hash and distribution lock hash. Regenerate
`make contracts-shadow` on the merged consumer revision for downstream
alignment. Embedded resources continue serving runtime validation offline.
