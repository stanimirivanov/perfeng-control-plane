# Mixed-family contract distribution shadow

## TL;DR

- Compare the embedded snapshot to the signed historical release with an explicit offline gate.
- The receipt identifies the consumer commit, both lockfile digests and every selected family member.
- Keep routine verification and runtime startup on the embedded files.

## Run the shadow

Stage both releases in a private directory using the exact tags in
[`contracts-distribution.lock.json`](../../contracts-distribution.lock.json).
Acquire the independently pinned verifier from the reviewed environment tool
bootstrap and use a trusted checkout of that bootstrap with its locked Python
dependencies already installed. The shadow restores the signed materializer
into a fresh temporary directory for each invocation.

```sh
assets=$(mktemp -d)
lock=contracts-distribution.lock.json
gh release download "$(jq -er .toolRelease "$lock")" \
  --repo stanimirivanov/perfeng-environment --dir "$assets/tool"
gh release download "$(jq -er .historyRelease "$lock")" \
  --repo stanimirivanov/perfeng-contracts --dir "$assets/history"
make contracts-shadow CONTRACTS_SHADOW_ASSETS="$assets" \
  CONTRACTS_BOOTSTRAP=/absolute/path/to/reviewed/perfeng-environment \
  CONTRACTS_VERIFIER=/absolute/path/to/trusted/gh > /tmp/control-plane-contract-selection.json
```

The restore and inspection steps use local inputs. The released inspector
authenticates each complete provider closure, including members outside the
control-plane selection. The shadow compares the independently signed
SHA-256 of every selected member with the checked-in ledger, the committed
snapshot and the working tree bytes. Missing, substituted or unreviewed
bytes stop the command before it emits a passing receipt.

Keep the receipt alongside the consumer commit it identifies and the reviewed
distribution lock it hashes. A downstream alignment check must use the receipt
for the actual revision it is aligning with. This is parity evidence only. The
[candidate loader check](contract-candidate-admission.md) and
[immutable Git source check](contract-source-admission.md) remain separate gates.
