# Control-plane engineering standards

## TL;DR

- Keep domain and application policy independent from HTTP, PostgreSQL,
  Kubernetes, S3, frameworks and vendors.
- Preserve explicit state, transaction, lease, revision, ownership and
  concurrency contracts.
- Prefer cohesive concrete Go code and small consumer-owned interfaces over
  speculative abstractions.
- Treat external input as untrusted and keep public failures classified and
  secret-safe.
- Test behavior and important failures deterministically, including races and
  real protocol boundaries where necessary.
- Lint findings inform review; never add suppression comments or artificial
  code merely to silence them.

## Policy strength

Normative terms use the meanings defined in
[CONTRIBUTING.md](../../CONTRIBUTING.md#policy-language-and-sources-of-truth).
MUST and MUST NOT are requirements; SHOULD and SHOULD NOT are strong defaults
that require a recorded reason to deviate; MAY is optional.

## Architecture and boundaries

Organize around run admission, lifecycle, reconciliation, artifacts, policies,
baselines and runtime composition rather than generic technical layers.

- Domain packages own vocabulary, invariants, transitions and policies and MUST
  NOT depend on HTTP, SQL, Kubernetes, S3 or configuration frameworks.
- Application composition coordinates use cases and adapters without moving
  adapter-specific parsing or error meaning into the domain.
- Inbound adapters validate protocol shape and translate to domain operations.
- Outbound adapters implement consumer-owned capabilities and translate domain
  values to storage, cluster or object-store representations.
- Transport DTOs, persisted rows, Kubernetes objects, registry documents and
  domain values remain distinct when their invariants or lifecycles differ.
- A package owns its writes. Cross-package behavior uses explicit APIs, not
  another package's private representation.

Use SOLID and patterns as reasoning tools, not quotas. Prefer direct code,
composition, explicit ownership and the simplest design preserving the boundary.
Do not add an interface, generic repository, manager, queue or cache without
current behavior that requires it.

## Go design

- Use the Go version in `go.mod` and pinned quality tools from repository
  configuration.
- Keep packages cohesive with small public surfaces and meaningful names.
- Define interfaces near consumers. Accept interfaces and return concrete types
  unless a public abstraction genuinely requires otherwise.
- Prefer concrete values and ordinary functions. Use generics only for a real
  type-safe algorithm or data structure, not to emulate inheritance.
- Make zero values useful only when doing so preserves invariants; otherwise use
  a validating constructor and keep invalid representations private.
- Pass `context.Context` first for request-scoped cancellation and deadlines.
  Do not store it in structs or use it as an argument bag.
- Return classified errors. Reserve panic for programmer invariants or startup
  failures where continuation is impossible.
- Document every package, exported declaration and interface method with a
  complete sentence explaining purpose and contract.

The detailed formatting, naming and documentation conventions already defined
in [CONTRIBUTING.md](../../CONTRIBUTING.md#go-house-style) remain authoritative.

## State, time and concurrency

- Model run states, failure codes, identifiers, revisions, hashes, durations
  and timestamps explicitly.
- Preserve event time, database time and observation time distinctions.
- Inject clocks, identifiers and external clients where determinism or policy
  depends on them.
- Keep goroutine ownership visible and bound concurrency, retries and queues.
- Child work belongs to a request, reconciliation attempt or runtime lifecycle
  and is cancelled with it.
- Document lease ownership, lock order, revision fencing, cancellation priority,
  retry safety and terminal failure.
- Never coordinate tests with sleeps when an event, barrier, controllable clock
  or observable state can prove the behavior.

## Errors and resources

- Distinguish invalid input, unauthorized or forbidden access, conflict,
  absence, unavailable dependencies, cancellation, timeout and internal defect.
- Preserve error classification with `errors.Is` and `errors.As` and wrap only
  with safe operational context.
- Never expose credentials, DSNs, tokens, lease values, raw rows, arbitrary
  backend messages or untrusted payloads through APIs or ordinary logs.
- Acquire files, rows, transactions, response bodies and goroutines with clear
  ownership and release them on every path.
- Check row iteration, explicit closes and transaction commits. Deferred
  rollback remains best-effort cleanup and MUST NOT replace the primary error.
- Keep database transactions short and free of remote calls.

## Contracts and untrusted input

HTTP requests, contract snapshots, mounted registries, object metadata,
artifact bytes, PostgreSQL records and Kubernetes resources are untrusted input.

- Parse and bound input at adapters, then construct validated domain values.
- Enforce authorization at the use case and protected data boundary, not only at
  HTTP routing.
- Treat APIs, embedded contract snapshots, persistence, migrations, object keys,
  registry formats and metrics as compatibility boundaries.
- Never change a published field's meaning in place. Use an explicit compatible
  transition and update `perfeng-contracts` when shared meaning changes.
- Exact retries compare stable identity and immutable content; key equality
  alone is insufficient.

## Testing and quality tools

- Test at the lowest boundary that proves behavior: domain tests, application
  tests with fakes, adapter integration tests, contract tests and bounded
  end-to-end acceptance.
- Defect fixes SHOULD begin with a failing regression test.
- Cover success, rejection, duplicate, stale revision, authorization, timeout,
  cancellation, lease loss, partial failure and recovery as applicable.
- Use disposable PostgreSQL or Kubernetes when mocks cannot reproduce
  constraints, transactions, locking, API defaulting or resource ownership.
- Run race tests for concurrent code and bounded native fuzzing for parsers.
- Coverage is diagnostic evidence, not a target.
- Lint output remains informational as documented in CONTRIBUTING. Findings are
  reviewed on their merits; no `nolint` comment, redundant explanation or
  artificial helper is added merely to make the report quiet.

## Documentation and dependencies

- Document public purpose, invariants, inputs, outputs, units, ownership, side
  effects, concurrency, security expectations and caller-actionable failures.
- Comments explain decisions and constraints, not syntax or standard-library
  behavior.
- Long documents follow the TL;DR policy in CONTRIBUTING.
- Dependencies require a current need, compatible license, maintained release,
  pinned resolution, security review and a replacement boundary proportionate
  to risk.
- Operational behavior changes update the relevant guide in the same pull
  request.

## Required verification

Run the exact checks in CONTRIBUTING plus PostgreSQL, Kubernetes, image or other
focused integration tests required by the affected boundary. Unavailable checks
follow the constrained-environment protocol and MUST NOT be reported as passed.
