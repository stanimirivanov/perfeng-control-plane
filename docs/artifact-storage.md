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
immutable reference.

## AWS SDK adapter

`S3Getter` implements `Getter` through the AWS SDK for Go v2 `GetObject`
operation. Its caller-defined `S3GetObjectAPI` interface contains only that
operation, allowing unit tests without a network service while remaining
directly compatible with `*s3.Client`.

The adapter:

- preserves caller cancellation and deadlines;
- maps modeled `NoSuchKey` and `NotFound` responses to `ErrObjectNotFound`;
- maps server faults, throttling and network failures to `run.ErrUnavailable`;
- exposes only a safe error code for other S3 API failures;
- never returns backend messages, object keys, endpoints or signed requests; and
- rejects nil or incomplete successful SDK responses.

Client construction remains explicit composition. The local SeaweedFS client
must use region `us-east-1`, its cluster-local base endpoint and path-style
addressing. Credentials must come from the runtime Secret/provider chain, never
artifact references, command arguments or committed configuration. Production
endpoint and transport policy require a separate deployment review.

## Remaining integration

This package intentionally does not construct the configured AWS SDK client,
load credentials, choose endpoints, parse raw-result or normalized-result
contracts, or change Run lifecycle state. Those responsibilities belong to
production composition and the raw/normalized collectors. Tests use injected
SDK and storage interfaces and never contact object storage.

References: [AWS SDK for Go v2 GetObject usage](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/using.html),
[client endpoint configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html),
and [error handling](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/handle-errors.html).
