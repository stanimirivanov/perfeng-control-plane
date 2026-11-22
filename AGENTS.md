# Repository agent guide

## Agent skills

Use `mattpocock/skills` as the primary workflow for planning, ticketing,
implementation, TDD, review, and pull requests. The skills take precedence where
contributor guidance covers the same steps. Follow the repository's technical
requirements, CI gates, and authorization boundaries.

### Issue tracker

Use GitHub Issues in `stanimirivanov/perfeng-control-plane`. See [issue tracker configuration](docs/agents/issue-tracker.md).

### Triage labels

Use the default Matt Pocock label mapping. See [triage labels](docs/agents/triage-labels.md).

### Domain docs

Single context with a root glossary and ADRs in `docs/adr/`. See [domain docs](docs/agents/domain.md).

## Repository responsibility

`perfeng-control-plane` owns authenticated run and baseline APIs, durable run
coordination, reconciliation policy, leases, Kubernetes execution orchestration,
artifact registration, runtime composition and control-plane persistence. It
does not own workload implementation, statistical analysis, shared contract
definitions or environment deployment composition.

- Shared interchange meaning belongs in `perfeng-contracts`.
- Runners and analysis behavior belong in their component repositories.
- Kubernetes packaging belongs in `perfeng-k8s`; environment selection belongs
  in `perfeng-environment`.
- Transport, database and Kubernetes representations MUST remain outside the
  domain contracts they implement.

## Architecture and implementation

- Dependencies point inward: adapters depend on domain/application contracts,
  not the reverse.
- Interfaces belong near consumers and document success, absence, failure,
  ownership and concurrency behavior.
- Preserve transaction boundaries, row-lock order, lease fencing, revision
  checks, clock-read placement and cancellation priority unless the issue
  explicitly changes and tests them.
- External calls MUST stay out of database transactions.
- Model states, failures, identifiers, revisions and time explicitly.
- Validate untrusted HTTP, JSON, registry, object-store and Kubernetes input at
  adapters; enforce lifecycle invariants in the domain.
- Public APIs and errors MUST not expose credentials, DSNs, tokens, driver
  details or arbitrary backend messages.

Follow the engineering and migration standards and the relevant operational
documentation.

The [coding harness guide](docs/development/harness.md) defines the command
tiers and feedback sensors. New packages MUST join the architecture
classification and new recurring guidance MUST be routed from the documentation
map.

## Quality and documentation

- Test observable behavior at the lowest boundary that proves it.
- Tests MUST be deterministic and use synchronization rather than sleeps.
- Run race tests for concurrency-sensitive behavior and real disposable
  PostgreSQL/Kubernetes tests where mocks cannot prove semantics.
- Exported declarations and interface methods MUST have useful Go documentation.
- Comments SHOULD explain decisions and constraints and SHOULD NOT narrate code
  or suppress a linter without an independently justified exception.
- Long documents MUST include the TL;DR required by CONTRIBUTING.

## Verification

Run `make verify` and the applicable additional checks documented in
[CONTRIBUTING.md](CONTRIBUTING.md#verification-and-constrained-environments).
Use the [documentation map](docs/README.md) to find affected standards, operational
guides and decisions. Report checks as passed, failed or unavailable.
