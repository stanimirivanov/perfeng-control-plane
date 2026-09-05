# Performance-policy validation boundary

`internal/policy` treats approved `PerformancePolicy` bytes as immutable but
still untrusted input. `Parse` accepts one bounded UTF-8 JSON document with
unique object keys and the exact closed v1 shape. Unknown, duplicate, malformed,
null and excessively nested fields fail validation rather than being ignored by
typed decoding.

The parser validates the policy identity and owner, non-blocking mode,
inconclusive missing-data behavior, unique rule IDs and metric selectors,
supported statistics, finite quality and SLO bounds, ordered SLO ranges, and
pinned regression references. Each rule must configure an SLO, a regression, or
both. Quality requirements are optional and must contain at least one bound.

`VerdictApprover` compares an `analysis-result/v1` manifest with the exact policy
bytes selected by the trusted report resolver. It requires matching policy
identity, version, SHA-256 digest and mode, matching test identity, and exactly
one report evaluation for every policy rule. The approved baseline-resolution
set must exactly cover the distinct baseline IDs and versions named by regression
rules. Metric selectors must match all three policy fields.

For a decisive quality result, the reported sample count and coefficient of
variation must satisfy the configured bounds. Decisive SLO results are
recomputed from the reported value with inclusive minimum and maximum bounds.
Decisive regression results are recomputed from candidate and reference values,
direction and absolute or relative practical difference. The reported effect
must match that calculation within relative tolerance `1e-9` and absolute
tolerance `1e-12`; reaching the practical-difference threshold is a failure.

An unconfigured SLO or regression must be `NOT_EVALUATED`. A configured
regression may be `INCONCLUSIVE` when no approved reference is available, but it
cannot be `NOT_EVALUATED`. The approver verifies report claims only. It does not
resolve policies, choose baselines, authorize producers, read artifacts or run
statistical tests; those responsibilities remain in the surrounding trusted
report composition.

The pinned policy schema and example are recorded in the contract snapshot lock.
Updates require an explicit contract provenance review and checksum change.
