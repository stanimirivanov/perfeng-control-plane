# Artifact object verification

PostgreSQL stores immutable artifact references; object storage contains the
referenced bytes. Neither the reference nor an S3 success response is sufficient
evidence by itself. `internal/objectstore.Reader` is the shared verification
boundary for future raw and normalized artifact collectors.

## Location policy

One reader instance is configured for one approved bucket. It accepts only
canonical locations shaped as:

~~~text
s3://<configured-bucket>/runs/<artifact-run-id>/<object-key>
~~~

HTTPS URLs, alternate buckets, endpoint ports in artifact URIs, credentials,
queries, fragments, encoded paths, traversal, repeated separators, another
Run's prefix and empty object keys are rejected before storage is contacted.
The configured S3 endpoint is a client concern and never comes from an artifact
reference or public Run request.

## Byte verification

`Reader.Read` first validates the immutable artifact reference and its configured
maximum size. The injected `Getter` receives only the parsed bucket and key. A
successful response must provide a body, exact content length and exact media
type. The reader then:

1. reads at most the declared size plus one byte;
2. closes the body and preserves read, close and cancellation errors;
3. rejects truncated or extended content; and
4. compares SHA-256 against the reference before returning bytes.

The configurable limit has a hard 256 MiB ceiling so a reference cannot turn a
control-plane verification attempt into an unbounded allocation. Larger result
streams require a future streaming verifier rather than raising this ceiling
without design review.

`ErrObjectNotFound` distinguishes storage visibility from
`ErrObjectMismatch`, which means that stored metadata or bytes contradict the
immutable reference. The S3 client adapter must map backend not-found responses
to the former and return safe operational errors for all other failures.

## Remaining integration

This package intentionally does not construct an AWS SDK client, load
credentials, choose endpoints, parse raw-result or normalized-result contracts,
or change Run lifecycle state. Those responsibilities belong to production
composition and the raw/normalized collectors. Tests use an injected in-memory
getter and never contact object storage.
