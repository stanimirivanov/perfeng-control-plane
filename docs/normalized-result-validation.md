# Normalized-result validation

The `internal/normalizedresult` package is the untrusted-input boundary for
`normalized-result/v1` envelopes produced by an analysis adapter. Parsing proves
contract structure and internal consistency; it does not approve the normalizer
or attest the external normalized artifact reference.

## Parsing rules

`normalizedresult.Parse` accepts envelope bytes together with the expected Run
ID and contracts bundle version. It rejects input unless all of these conditions
hold:

- the document is non-empty, valid UTF-8, no larger than 16 MiB and within the
  shared nesting limit;
- it contains one JSON value with no trailing content or duplicate object keys,
  including inside open metadata;
- closed objects use exact, case-sensitive contract properties;
- envelope identity, workload, producer, measurement window and creation time
  satisfy the same rules as `raw-result/v1`;
- at least one unique raw source artifact belongs to the Run;
- at least one result/v2 record belongs to the Run, and metric names are unique;
- metric names, directions and optional types match the contract;
- a supplied sample count is positive, while absent or null remains unavailable;
- all numeric values are finite, and percentiles, standard deviation and
  coefficient of variation are non-negative; and
- optional legacy threshold objects have their required `passed` value and
  exact result fields.

Metadata remains an open JSON object as defined by result/v2. Unknown fields are
allowed only inside that object; duplicate keys and invalid JSON are still
rejected. The parser does not infer statistics, sample counts, thresholds or
verdicts.

## Trust boundary

A valid envelope does not establish that its source bytes were actually used,
that the producer image ran, or that the normalized object matches its external
checksum and size. `VerifiedNormalizedCollector` independently:

1. obtains the output's immutable artifact reference from trusted execution
   state;
2. verifies its object bytes with the object-storage reader;
3. parses those verified bytes;
4. retrieves and verifies the already-registered raw-result manifest again;
5. requires its exact source set, contracts version, test, workload and
   measurement window to match the normalized envelope;
6. rejects a normalized envelope created before that raw manifest;
7. authorizes the normalizer and accepted execution context; and
8. returns the reference only after every check succeeds.

Output-reference discovery remains an injected boundary. The resolver must use
trusted orchestration state rather than public request fields or object-store
listing. `registry.ReportPolicyRegistry` supplies normalizer approval from
reviewed startup entries. It requires the exact principal, accepted request,
contracts version, test, workload, digest-pinned normalizer and complete source
set. The collector itself binds dynamic provenance to the verified raw manifest
as an order-independent set of complete artifact records.

An absent publication or object after Kubernetes reports Job success remains a
retryable `ErrAnalysisPending`, allowing for bounded storage visibility delay.
An invalid reference, changed bytes, malformed envelope, source mismatch or
rejected provenance becomes `ErrAnalysisFailed`. Conflicting classifications
fail with validation rather than selecting a favorable interpretation. The raw
manifest is different: it was already registered as durable evidence before
ANALYZING, so its absence, changed bytes or invalid contents are definitive
analysis failures rather than publication lag.

Scientific quality, SLO evaluation and regression decisions remain downstream
analysis responsibilities.
