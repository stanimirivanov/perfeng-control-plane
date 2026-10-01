# Control-plane coding harness

## TL;DR

- Use the [documentation map](../README.md) for task-specific guidance and run
  `make bootstrap` once to resolve pinned dependencies.
- `make verify` is the fast, network-independent inner loop after bootstrap;
  `make validate` is the complete non-live acceptance suite.
- PostgreSQL, Kubernetes, and image checks are explicit specialized targets and
  run only when the changed boundary requires them.
- Harness failures must be actionable, and an unavailable check is reported as
  not run rather than weakened or claimed as passed.
- Recurring review findings should become clearer guidance, a deterministic
  sensor, or an explicit reviewed exception.

## Purpose

The harness combines feed-forward guidance with deterministic feedback.
Guidance explains intent and constraints before a change; repository-owned
commands detect drift after each edit. The root [Makefile](../../Makefile) and
CI workflows are the executable command surface.

## Cost tiers

| Tier | Timing | Commands and dependencies |
|:--|:--|:--|
| T0 — route | Before editing | Read CONTRIBUTING and the relevant rows in the documentation map. |
| T1 — inner loop | During implementation | Focused tests, then `make verify`; no network after bootstrap. |
| T2 — acceptance | Before handoff | `make validate`; race, fuzz, vulnerability, and license checks cost more CPU or network access. |
| T3 — specialized | When required by the boundary | `make db-validate`, `make kubernetes-validate`, or `make image`; requires an explicitly configured disposable service or Docker. |

Cost does not make an applicable check optional. Follow the
[constrained-environment protocol](../../CONTRIBUTING.md#verification-and-constrained-environments)
when a check cannot run.

## Command inventory

| Command | Evidence produced |
|:--|:--|
| `make bootstrap` | Pinned application and quality-tool modules are resolved. |
| `make doctor` | Effective Git, Go, and Make toolchains are reported. |
| `make build` | All commands compile without writing repository artifacts. |
| `make binaries` | Runnable commands are written under ignored `bin/`. |
| `make docs-check` | Local links, TL;DR policy, ADR metadata/indexes, and contribution templates are coherent. |
| `make architecture-check` | Every production package is classified and core dependencies still point inward. |
| `make check` | Formatting, documentation, architecture, lint configuration/reporting, workflows, and module state are coherent. |
| `make test` | Ordinary deterministic tests pass without cached results. |
| `make verify` | Build, checks, and ordinary tests close the inner loop. |
| `make race` | The complete Go suite passes with the race detector. |
| `make fuzz` | The bounded JSON parser fuzz target passes. |
| `make supply-chain` | Known-vulnerability and license policy checks pass for product and tool graphs. |
| `make validate` | The complete non-live acceptance aggregate passes. |
| Specialized targets | The affected database, Kubernetes API, or image boundary is exercised explicitly. |

The architecture sensor deliberately describes today's behavior-preserving
boundary. The broad edge layer is transitional: reconciliation and registry
code currently references concrete Kubernetes or object-store types. The gate
prevents core packages from acquiring infrastructure authority and requires new
packages to be classified; it does not claim that the remaining edge is already
a strict hexagonal design. Tightening that boundary belongs in focused,
behavior-preserving refactoring changes.

## Steering the harness

When a defect or review finding recurs:

1. Clarify the narrow canonical guide if intent was ambiguous.
2. Add the cheapest deterministic sensor when structure can be checked.
3. Add a focused behavioral regression test when observable behavior escaped.
4. Record an owned exception when automation would be noisy or misleading.

New sensors MUST be actionable, deterministic at their stated tier, tested on
failure behavior, and invoked by an aggregate command. They MUST NOT depend on
ambient unpinned tools, silently rewrite expected behavior, or turn an
unavailable integration into a passing result.
