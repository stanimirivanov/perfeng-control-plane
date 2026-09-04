# Analysis-result validation boundary

`internal/analysisresult` treats `analysis-result/v1` bytes as untrusted input.
`Parse` accepts only one bounded UTF-8 JSON document with unique object keys and
the exact closed contract shape. Unknown, duplicate, malformed and excessively
nested fields fail validation rather than being ignored by typed decoding.

The parser requires the expected Run and contracts bundle version, a
digest-pinned producer, a non-blocking policy identity, one normalized candidate,
unique normalized references from other Runs, and at least one unique policy-rule
evaluation. Metric selectors, verdict states, explanations, sample counts,
variability, measured values and comparison evidence are checked for their
contract ranges and internal relationships. A non-passing quality result cannot
produce a decisive SLO or regression outcome. A decisive regression must identify
one of the report's reference artifacts and include finite candidate, reference
and practical-effect values plus a versioned method.

An empty reference list is valid. Missing approved baseline evidence is expressed
as an `INCONCLUSIVE` regression with a reason; it is not a parser error or a Run
infrastructure failure.

Structural validity is not trust. Before registration, composition must also:

- read bytes through the approved immutable object-storage boundary;
- require the candidate artifact to equal the one registered for the Run;
- resolve and authorize the exact policy identity and checksum;
- authorize every selected reference and its baseline lifecycle state;
- attest the report producer image and publication context; and
- validate or recompute policy rule coverage and verdict arithmetic.

`VerifiedReportCollector` composes the approved object reader and this parser,
requires the parsed candidate to equal the normalized artifact handed to
reporting, and returns the immutable report reference only after an injected
approver accepts the remaining policy, reference, producer and verdict claims.
The parser itself performs no network, database, Kubernetes or object-storage
I/O. Concrete publication resolution and approval remain external adapters.
