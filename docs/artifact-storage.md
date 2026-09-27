# Artifact object verification

## TL;DR

- PostgreSQL references are accepted only after exact object bytes, metadata,
  size and SHA-256 are verified through the shared reader boundary.
- Artifact locations are restricted to the configured S3 bucket and the owning
  Run's canonical namespace.
- Discovery uses deterministic keys and bounded metadata; it never lists or
  follows caller-supplied URLs.
- Missing objects, unavailable storage, invalid locations and corrupt evidence
  remain distinct secret-safe failures.
- Native upload authorization signs only one server-selected PUT key, content
  type and full-object checksum and returns no reusable storage credentials.
- Storage authentication and endpoint policy come from trusted runtime
  configuration, not artifact documents.

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

## Raw-manifest discovery

`RawManifestResolver` inspects only `runs/<run-id>/raw-result.json` in its
configured bucket. It never lists objects or accepts a location from the Run
request. The resolver is pinned to either `raw-result/v1` or `raw-result/v2` and
requires exactly three publisher-defined metadata values: `artifact-id`,
`sha256`, and `format`. Object size and `application/json` media type come from
the storage response. Together these fields form the complete artifact
reference.

`S3MetadataInspector` adapts the AWS SDK `HeadObject` operation, copies returned
metadata, preserves cancellation, classifies missing publication separately
from transient storage failure, and redacts backend details through the shared
S3 error classifier. Discovery metadata is still a producer claim. The
collector passes the resulting reference to `Reader`, which downloads the object
and verifies its response metadata, byte count, and SHA-256 before parsing or
approval. A changed object therefore cannot pass merely because its discovery
metadata was accepted.

## Normalized-manifest discovery

`NormalizedManifestResolver` applies the same closed metadata contract to the
deterministic `runs/<run-id>/normalized-result.json` publication. It accepts
only `normalized-result/v1` JSON metadata and returns a normalized artifact
reference without listing the bucket or reading object bytes. The normalized
collector independently downloads and verifies that reference before parsing
or approval.

## Report-manifest discovery

`ReportManifestResolver` applies the same closed metadata contract to the
deterministic `runs/<run-id>/analysis-result.json` publication. It accepts only
`analysis-result/v1` JSON metadata and returns a normalized artifact reference;
the artifact contract distinguishes the report from its normalized candidate by
format. Discovery uses only the principal and immutable Run context. The
candidate is not needed to locate the object and remains independently bound by
the verified report collector after byte verification and parsing.

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

`S3ArtifactAccess` wires one already-configured SDK client to the metadata
inspector, getter, raw, normalized and report stable-key resolvers, and bounded
reader. Its single policy fixes the bucket, accepted raw-manifest generation and
maximum read size consistently across those adapters. It implements the
discovery and reader interfaces consumed by all verified collectors.

## Native upload presigning

`S3UploadPresigner` adapts the AWS SDK v2 `PresignPutObject` operation to the
native-worker signing boundary. It accepts only a validated durable reservation
and the configured artifact bucket. The SDK input fixes the reservation's exact
object key, content type and base64-encoded full-object SHA-256. It deliberately
omits a request body and content length: the signed checksum prevents a changed
payload, while the assigned size is enforced by reservation policy and must be
verified again during artifact registration.

The adapter bounds the requested signature lifetime by the durable reservation,
leaves a one-second signing margin where possible, and parses the resulting
`X-Amz-Date` and `X-Amz-Expires` values. A URL whose actual expiry exceeds the
lease-bounded reservation is rejected. It also requires:

- HTTPS with no embedded credentials or fragment;
- `PUT` for the exact configured bucket and server-selected object key, in
  either path-style or virtual-hosted S3 form;
- the `PutObject` operation identifier emitted by the SDK;
- SigV4 with the signed-header set
  `content-type;host;x-amz-checksum-sha256`;
- a single SDK-signed host matching the URL host; and
- exactly the content-type and checksum headers returned by the Native Worker
  API contract.

The SDK signs `Host` internally, but the worker does not set it explicitly;
therefore the delivered closed header object contains only `content-type` and
`x-amz-checksum-sha256`. Presigner errors, credentials and unexpected SDK output
are reduced to safe classifications. Context cancellation and deadline identity
are preserved.

The native upload-grant issuer commits the non-secret PostgreSQL reservation
before invoking this adapter. Presigning is consequently outside the database
transaction. If credential resolution or signing fails, an identical retry can
reuse the durable grant identity without persisting a URL, query or signed
header.

## Native upload verification

`NativeUploadVerifier` consumes only the server-selected durable candidate and
the configured artifact bucket. It requests that exact key, compares response
size and media type, then streams at most the declared size plus one byte through
SHA-256. It never buffers the complete native artifact in control-plane memory.
Missing objects, storage failures, read or close errors, cancellation, extra or
missing bytes and checksum disagreement preserve their existing safe
classifications.

The registration use case deliberately spans two short database transactions.
The first selects the immutable grant and reviewed artifact semantics, then
releases all locks before the verifier calls S3. The second reacquires current
lease authority and the Run revision fence before persisting the generated
artifact identity. This avoids remote calls in transactions while preventing a
worker whose lease changed during verification from accepting stale evidence.
The accepted S3 URI contains only bucket and canonical object key; endpoint,
credentials, presigned query and signed headers are excluded.

## Remaining integration

The executable composition root now constructs artifact readers and the
presign client and native upload verifier from one configured AWS SDK client.
The authenticated Native Worker API exposes the upload-grant operation and
returns its sensitive response with `Cache-Control: no-store`. The durable,
lease-fenced registration use case and its PostgreSQL adapter are implemented,
but the registration HTTP operation is not composed into the executable yet.
This package still does not load credentials, choose endpoints, parse
raw-result or normalized-result or analysis-result contracts, or change Run
lifecycle state. Parsing and lifecycle changes belong to the reconciliation
collectors. Tests exercise the real SDK presign middleware with static fake
credentials and an inert HTTPS endpoint, so they perform no network request.

The deployment must still provide a worker-reachable HTTPS S3 endpoint. An
HTTP-only cluster-internal endpoint remains valid for existing artifact reads,
but its generated upload authorization is rejected before delivery. Exposing
native artifact registration over authenticated HTTP and implementing the
worker-side upload flow remain later M03 slices.

The application composition layer combines this access bundle with immutable
registry trust, baseline persistence and one pinned contracts version to create
the three verified collectors. Registry population and lifecycle wiring remain
separate startup work.

References: [AWS SDK for Go v2 GetObject usage](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/using.html),
[client endpoint configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html),
and [error handling](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/handle-errors.html).
