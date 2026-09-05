# Raw-result manifest validation

The `internal/rawresult` package is the untrusted-input boundary for
`raw-result/v1` manifests. It validates a producer's claims before a collector
can approve storage or register artifact references.

## Parsing rules

`rawresult.Parse` accepts manifest bytes together with the expected Run ID and
contracts bundle version. It rejects input unless all of these conditions hold:

- the document is non-empty, valid UTF-8 and no larger than 1 MiB;
- it contains one JSON value with no trailing content or duplicate object keys;
- object properties use the exact contract spelling and no unknown properties;
- `schemaVersion` is `1`, `kind` is `RawResult`, and the Run and contracts
  identities match the trusted context supplied by the caller;
- test, workload and producer identities, versions, hashes and immutable image
  reference have the required syntax;
- timestamps use the contract form with at most microsecond precision, the
  measurement start precedes its end, and creation is not before the end;
- at least one artifact is declared, every artifact is raw and belongs to the
  same Run, and its complete artifact reference is valid; and
- artifact IDs and object locations are unique within the manifest.

The parser follows the generic contract's minimum of one artifact. The current
k6 adapter emits summary and point artifacts, but requiring exactly those two
formats is producer policy and does not belong in this contract parser.

Invalid input returns the shared validation sentinel without exposing decoder
or producer-controlled details. A successful result does not alias the input
byte slice.

## Trust boundary

Structural validity is not provenance approval. In particular, a parsed
manifest does not prove that:

- the producer, test or workload is approved for the Run;
- the manifest came from an approved immutable object;
- a declared object exists in an approved bucket and Run namespace; or
- the declared size and checksum match the remote bytes.

The eventual collector must obtain the manifest through a trusted publication
reference, approve its claims against the accepted Run, and use the object-store
verification boundary for every declared artifact before registration. The
current producer contract does not yet provide a trusted manifest receipt or a
deterministic manifest identity, so this package deliberately does not locate
manifests or assign their artifact IDs.

`reconcile.VerifiedRawCollector` connects this parser to the object-storage
reader without weakening that boundary. It receives a complete immutable
manifest reference from an injected trusted resolver, verifies and parses the
manifest, invokes an injected provenance approver, validates the manifest and
source references as one set, and verifies every source object's bytes.

`registry.ReportPolicyRegistry` is the concrete provenance approver. Its
reviewed startup entry pins the contracts bundle, test, workload and
digest-pinned raw producer to the same principal and resource context used at
Run admission. It validates the manifest again at the adapter boundary and
rejects any valid but unauthorized claim. Trusted manifest publication
resolution remains an external adapter; the collector does not infer a reference
from an object key.
