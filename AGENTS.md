# Repository working agreement

## TL;DR

- Read `CONTRIBUTING.md`; it is the canonical workflow policy.
- Preserve API, persistence, lifecycle, lease and concurrency contracts.
- Keep lint findings informational and never add suppression comments merely to
  make a report quiet.
- Run the exact Go checks and report their results honestly.

This file is the concise, tool-facing entry point for contributors and coding
agents. [CONTRIBUTING.md](CONTRIBUTING.md) is the canonical workflow policy.
Normative terms such as MUST, SHOULD and MAY have the meanings defined there.

## Before changing anything

1. You MUST read [CONTRIBUTING.md](CONTRIBUTING.md), especially its ambiguity,
   issue-timing, verification and external-side-effect rules.
2. You MUST inspect the branch and working tree and preserve pre-existing work.
3. You MUST use the [documentation map](docs/README.md) to read the relevant
   engineering standard, operational guide and accepted ADR. Do not bulk-read
   unrelated guides or decisions.
4. You MUST use repository-local checks and MUST NOT claim that an unavailable
   or unexecuted check passed.

If code, contracts, documentation and an accepted decision disagree, you MUST
NOT silently pick one. Correct an obvious local defect or propose a superseding
ADR. Ordinary ambiguity follows CONTRIBUTING.

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

## Shape of work

- Each task and pull request MUST deliver one coherent, independently
  reviewable outcome.
- Prefer a thin end-to-end behavior over unused layers or speculative ports.
- Behavioral changes, migrations, dependency updates and broad cleanup MUST
  stay separate unless inseparable for correctness.
- Scope, assumptions, API/contract effects, migration, concurrency, rollout,
  rollback and verification evidence MUST be explicit.

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

- Test observable behavior at the lowest boundary that proves it. Defect fixes
  SHOULD begin with a failing regression test.
- Tests MUST be deterministic and use synchronization rather than sleeps.
- Run race tests for concurrency-sensitive behavior and real disposable
  PostgreSQL/Kubernetes tests where mocks cannot prove semantics.
- Exported declarations and interface methods MUST have useful Go documentation.
- Comments SHOULD explain decisions and constraints and SHOULD NOT narrate code
  or suppress a linter without an independently justified exception.
- Long documents MUST include the TL;DR required by CONTRIBUTING.

## Completion

The canonical required checks and completion-report format are defined in
[CONTRIBUTING.md](CONTRIBUTING.md). Follow them after every work item.
