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

1. obtain the output's immutable artifact reference from trusted execution
   state;
2. verify its object bytes with the object-storage reader;
3. parse those verified bytes;
4. approve the normalizer and compare envelope provenance, window and exact
   source references with the accepted analysis input; and
5. return the reference only after every check succeeds.

Output-reference discovery and provenance approval remain injected boundaries.
The resolver must use trusted orchestration state rather than public request
fields or object-store listing. The approver owns catalogue and image policy;
the collector itself requires the parsed source references to equal the accepted
analysis input as an order-independent set of complete artifact records.

An absent publication or object after Kubernetes reports Job success remains a
retryable `ErrAnalysisPending`, allowing for bounded storage visibility delay.
An invalid reference, changed bytes, malformed envelope, source mismatch or
rejected provenance becomes `ErrAnalysisFailed`. Conflicting classifications
fail with validation rather than selecting a favorable interpretation.

Scientific quality, SLO evaluation and regression decisions remain downstream
analysis responsibilities.
