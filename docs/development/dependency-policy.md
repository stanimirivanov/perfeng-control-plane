# Control-plane dependency policy

## TL;DR

- The root Go module contains product and test dependencies; development tools
  live in isolated, pinned modules under `tools/`.
- Admit a dependency only for current behavior after reviewing maintenance,
  license, security, compatibility, and replacement cost.
- Exact module files and container digests are supply-chain controls; do not
  replace them with floating versions.
- Run `make supply-chain` for dependency changes and record any unavailable
  network-backed scan explicitly.
- GitHub Actions use semantic major tags; OCI images retain digest pins.

## Dependency graphs

The root `go.mod` is the deployable product graph. `tools/quality` owns the
linter, vulnerability scanner, and license checker. `tools/actionlint` owns the
workflow validator in a separate module because its YAML dependency is not
build-compatible with the quality-tool graph. Tool dependencies MUST NOT enter
the product module or runtime image.

All graphs are pinned by checked-in `go.mod` and `go.sum` files. Update each
graph independently and verify it through the root Makefile. Do not use ambient
global tools as acceptance evidence.

## Admission and updates

Before adding or upgrading a dependency, review:

- the current behavior that requires it and whether the standard library or an
  existing dependency already provides that behavior;
- maintenance activity, release and vulnerability history, and transitive size;
- license compatibility for the way the binary or tool is distributed;
- API stability, supported Go version, and replacement boundary; and
- effects on contracts, images, deployment, and operators.

Dependency-only changes SHOULD remain separate from behavioral changes unless
inseparable for correctness or security. Review changed transitive modules and
new diagnostics, not only the requested direct version.

## Vulnerabilities and licenses

`make vuln` scans the application and both development-tool graphs with the
pinned scanner. Its vulnerability database may require network access and a
clean result is evidence at scan time, not proof of future absence.

`make license` applies an explicit permissive-license allowlist to product and
tool dependencies. Tool-only exceptions are documented beside the Makefile
arguments and do not authorize those packages in the runtime graph. A new or
unknown license requires review; do not broaden the allowlist simply to make a
check pass.

Fix confirmed findings through reviewed dependency or toolchain updates. Do not
hide findings with exclusions lacking an owner, rationale, and review trigger.

## Automation policy

Dependabot may propose grouped updates for each module and GitHub Actions.
Those pull requests still require the normal checks and review. The repository
does not use GitHub's dependency-review action because it is not enabled for
this repository; local and CI supply-chain commands remain authoritative.

GitHub Action references use semantic major tags, for example
`actions/checkout@v7`, so routine patch maintenance does not create opaque hash
churn. Container base images and disposable integration images are a different
trust boundary and SHOULD remain pinned by immutable OCI digest where the
repository already records one.
