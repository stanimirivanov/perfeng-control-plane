# Control-plane developer quickstart

## TL;DR

- Install Git, GNU Make, and the Go version declared in `go.mod`.
- Run `make bootstrap`, then `make doctor` and `make verify`.
- Use `make validate` before handoff and add the specialized PostgreSQL,
  Kubernetes, or image target when the changed boundary requires it.
- Never point integration checks at production resources.

## Setup

From the repository root:

```sh
make bootstrap
make doctor
make verify
```

`make bootstrap` downloads the exact application and repository-tool module
graphs. Go's default `GOTOOLCHAIN=auto` can obtain the `go.mod` toolchain; CI
uses the exact configured version with automatic switching disabled.

Race detection requires a supported C compiler. Image checks require Docker.
PostgreSQL and Kubernetes checks require disposable loopback resources prepared
as described in their operational guides.

## Daily loop

Run the narrowest relevant package test while editing, then close the local loop:

```sh
make fmt
make verify
```

Formatting changes files and must be reviewed. `make verify` does not require
network access after a successful bootstrap. Before handoff run:

```sh
make validate
```

For persistence work, set `PERFENG_TEST_DATABASE_URL` to an authorized
loopback-only disposable PostgreSQL administrative database and run
`make db-validate`. For Kubernetes API work, create the pinned disposable kind
cluster named `perfeng-test`, set `PERFENG_TEST_KUBECONFIG` to its loopback
kubeconfig, and run `make kubernetes-validate`.
Image-affecting work runs `make image`.

## Troubleshooting

- Run `make doctor` and compare the reported versions with repository files.
- Re-run `make bootstrap` after a checked-in module change.
- Do not install `@latest` quality tools or substitute a global linter.
- If a required check cannot run, follow CONTRIBUTING's reporting protocol;
  never report it as passed.
- Keep generated binaries, caches, credentials, DSNs, kubeconfigs, and evidence
  outside Git.
