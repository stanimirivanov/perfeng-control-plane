# Control-plane documentation map

## TL;DR

- Read [CONTRIBUTING.md](../CONTRIBUTING.md) for workflow policy, then use this
  map to load only the guidance relevant to the task.
- The root [README](../README.md) defines current product behavior and package
  ownership; accepted [ADRs](decisions/README.md) record durable decisions.
- Development guides explain implementation and verification practice without
  overriding product behavior or accepted decisions.
- Update this map when a canonical guide moves or a recurring task gains a new
  source of truth.

## Route by task

| Task or changed boundary | Read first | Verify or consult |
|:--|:--|:--|
| Contributor workflow or issue preparation | [CONTRIBUTING.md](../CONTRIBUTING.md) | [Harness guide](development/harness.md) |
| Build, CI, repository policy, or local setup | [Harness guide](development/harness.md), [developer quickstart](development/developer-quickstart.md) | `make docs-check`, `make verify`, `make validate` |
| Go implementation, package ownership, or concurrency | [Engineering standards](development/engineering-standards.md), [package boundaries](../README.md#boundaries) | `make architecture-check`, `make verify`, `make race` |
| Dependencies, licenses, or vulnerability response | [Dependency policy](development/dependency-policy.md) | `make supply-chain` |
| PostgreSQL schema, migrations, or durable state | [SQL migration policy](development/sql-migrations.md), [PostgreSQL guide](postgresql.md) | `make db-validate` |
| Reconciliation or Kubernetes execution | [Reconciliation guide](reconciliation.md), [Kubernetes dispatch](kubernetes-dispatch.md) | `make kubernetes-validate` when the live API boundary changes |
| Contained Kubernetes benchmarks | [Benchmark admission, reservations and cleanup](kubernetes-benchmarks.md), [ADR-0006](decisions/0006-reserve-benchmark-targets-before-dispatch.md), [ADR-0007](decisions/0007-fence-and-verify-benchmark-cleanup.md) | `make db-validate`, `make kubernetes-validate` |
| Artifact storage or result validation | [Artifact storage](artifact-storage.md), the relevant raw/normalized/analysis validation guide | Focused adapter tests and `make verify` |
| Registry or performance-policy behavior | [Registry guide](report-policy-registry.md), [policy validation](performance-policy-validation.md) | Registry and policy tests |
| Performance catalog contract adapters | [Pinned catalog conformance](performance-catalog-contract.md), [ADR-0009](decisions/0009-pin-performance-catalog-contracts.md) | Pinned schema, provenance and cross-language digest tests |
| Authorized performance catalog publication | [Publication guide](performance-catalog.md), [ADR-0010](decisions/0010-publish-authorized-performance-catalog.md) | Registry, HTTP, authentication and composition tests; `make validate` |
| Performance integration wire boundary and delivery sequence | [Integration guide](performance-integration.md), [ADR-0011](decisions/0011-pin-performance-integration-contracts.md) | Pinned exchange, offline schema and architecture tests; `make validate` |
| Reviewed integration admission bindings | [Integration guide](performance-integration.md#reviewed-admission-bindings), [ADR-0012](decisions/0012-bind-integration-admission-to-reviewed-registry.md) | Registry resolution, baseline and configuration tests; `make validate` |
| Baseline behavior or administration | [Baseline lifecycle](baseline-lifecycle.md), [baseline administration](baseline-administration.md) | Domain, HTTP, and PostgreSQL tests as applicable |
| Durable technical decision | [ADR index](decisions/README.md) | ADR template and supersession rules |
| Native Worker discovery or manual recovery | [Discovery guide](native-worker-discovery.md), [ADR-0005](decisions/0005-serialize-native-worker-discovery.md) | HTTP/application tests and `make db-validate` |
| Image build or publication | [README development section](../README.md#development), publish workflow | `make image` |
| Vulnerability reporting | [SECURITY.md](../SECURITY.md) | Repository owner |

## Authority

[CONTRIBUTING.md](../CONTRIBUTING.md) is the canonical workflow policy.
[AGENTS.md](../AGENTS.md) is the concise tool-facing entry point. The root
[README](../README.md) describes supported behavior and current ownership.
Accepted ADRs govern their decisions until superseded. Narrow operational
guides govern the boundary they explicitly document.

Read only the rows relevant to the change. Do not bulk-load every operational
guide or ADR. Surface conflicts through CONTRIBUTING's ambiguity protocol.

## Keeping the map current

A change MUST update this page when it adds, renames, moves, or removes a
canonical document or creates a recurring task without a clear route.
`make docs-check` validates repository-local links and mechanical documentation
policy; semantic accuracy remains a review responsibility.
