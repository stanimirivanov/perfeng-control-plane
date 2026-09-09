# Runtime configuration

The future control-plane executable reads deployment configuration from its
process environment. Configuration parsing is separate from dependency
construction so malformed or contradictory policy fails before any network
client is created.

## Artifact storage

All artifact-storage policy values are explicit:

| Variable | Meaning |
| --- | --- |
| `PERFENG_S3_REGION` | AWS signing region; required even for an S3-compatible service |
| `PERFENG_S3_ENDPOINT` | Optional absolute S3-compatible endpoint origin |
| `PERFENG_S3_PATH_STYLE` | Exactly `true` or `false` |
| `PERFENG_S3_ALLOW_HTTP` | Explicit consent for an `http` endpoint |
| `PERFENG_S3_MAX_ATTEMPTS` | Total SDK attempts per S3 request, from 1 through 10 |
| `PERFENG_ARTIFACT_BUCKET` | Approved artifact bucket |
| `PERFENG_RAW_MANIFEST_FORMAT` | Accepted raw manifest generation |
| `PERFENG_ARTIFACT_MAX_READ_BYTES` | Positive bounded object-read limit |

An absent endpoint selects normal AWS endpoint resolution and requires
`PERFENG_S3_ALLOW_HTTP=false`. A configured endpoint must contain only a scheme,
host and optional port. User information, paths, queries and fragments are
rejected. HTTPS is the default; HTTP is accepted only when the explicit flag is
true. This supports isolated local SeaweedFS without silently weakening a
production deployment.

Credentials are not part of this configuration value. `NewArtifactStorage`
loads the AWS SDK's standard credential provider chain, populated by the runtime
Secret or workload identity. It applies the configured region, standard retry
mode, maximum attempts, endpoint and addressing style before composing
`S3ArtifactAccess`. Loading configuration may inspect environment variables or
shared files, but it performs no S3 request. Provider errors are redacted;
caller cancellation and deadlines retain their standard identity.

The loader produces `S3ClientConfig` together with the validated
`S3ArtifactAccessConfig`. Authentication and registry settings remain separate
executable-composition inputs.

## Artifact verification

| Variable | Meaning |
| --- | --- |
| `PERFENG_CONTRACTS_VERSION` | Exact contracts bundle version expected in raw, normalized and report evidence |

The contracts version uses numeric `x.y.z` syntax and has no `latest` fallback.
`NewArtifactCollectors` binds one validated version to raw-manifest,
normalized-result and analysis-report verification. It combines deterministic
publication discovery and verified reads with immutable registry approval,
approved baseline resolution and independent verdict checking. Construction
does not read object storage, query the registry or database, or parse evidence.
The registry entries populated at startup must authorize the same contracts
version; that cross-check belongs to registry loading.

## PostgreSQL

`PERFENG_DATABASE_URL` is required and treated as a secret. Configuration and
connection errors never include its value. `NewDatabase` opens the existing
bounded PostgreSQL repository without applying migrations; schema migration
remains the responsibility of the dedicated command and deployment step.

## HTTP server

The HTTP listener requires all of the following explicit settings:

| Variable | Meaning |
| --- | --- |
| `PERFENG_HTTP_ADDRESS` | Listener host and non-zero port, such as `:8080` |
| `PERFENG_HTTP_READ_HEADER_TIMEOUT` | Maximum duration for request headers |
| `PERFENG_HTTP_READ_TIMEOUT` | Maximum duration for the complete request read |
| `PERFENG_HTTP_WRITE_TIMEOUT` | Maximum response-write duration |
| `PERFENG_HTTP_IDLE_TIMEOUT` | Keep-alive idle limit |
| `PERFENG_HTTP_SHUTDOWN_TIMEOUT` | Graceful-shutdown budget |
| `PERFENG_HTTP_MAX_HEADER_BYTES` | Header limit from 1024 through 1048576 bytes |

Durations use Go duration syntax, must be positive and cannot exceed ten
minutes. `NewHTTPServer` applies the listener policy to a required handler but
does not bind a socket or start serving. The shutdown timeout remains process
orchestration policy and is consumed when the executable is introduced.

## Reconciliation worker

The worker requires explicit identity, concurrency and lease policy:

| Variable | Meaning |
| --- | --- |
| `PERFENG_WORKER_ID` | Stable worker identity used for lease ownership |
| `PERFENG_WORKER_CAPACITY` | Concurrent reconciliation limit from 1 through 100 |
| `PERFENG_WORKER_LEASE_TTL` | Whole-second lease lifetime from 5 seconds through 5 minutes |
| `PERFENG_WORKER_RENEW_EVERY` | Positive renewal interval no greater than half the lease lifetime |
| `PERFENG_WORKER_POLL_EVERY` | Positive work-discovery interval no greater than 5 minutes |
| `PERFENG_WORKER_FAILURE_DELAY` | Whole-second failure delay from zero through 5 minutes |

The loader returns the existing `worker.Config`, whose validation is shared by
the loader and worker constructor.

## Kubernetes

The control plane uses in-cluster service-account credentials and requires:

| Variable | Meaning |
| --- | --- |
| `PERFENG_KUBERNETES_NAMESPACE` | DNS-label namespace containing managed Jobs and Pods |
| `PERFENG_KUBERNETES_REQUEST_TIMEOUT` | Positive per-request timeout no greater than 10 minutes |
| `PERFENG_KUBERNETES_QPS` | Client-side rate from 0.1 through 100 requests per second |
| `PERFENG_KUBERNETES_BURST` | Client-side burst from 1 through 1000 requests |

`NewKubernetesClients` copies the in-cluster REST configuration, applies these
bounds and returns only namespaced Job and Pod interfaces. Construction reads
service-account configuration but makes no API-server request. Loader and
client-construction failures are redacted while cancellation is preserved.

`NewKubernetesControllers` binds those clients and their namespace to the
execution dispatcher, termination verifier, normalization dispatcher and
reporting dispatcher. It rejects incomplete client bundles and constructs no
Kubernetes resources. The returned controllers remain inert until a
reconciliation operation invokes them.

`NewKubernetesExecutors` composes the normalization and reporting lifecycle
adapters from immutable registry trust, the baseline repository, these
controllers, verified artifact collectors and reviewed Job configuration. The
analysis configuration provides its reusable template, named normalizer and
input-staging init container, and fixed manifest, source and output paths. The
report configuration provides its reusable template, named input-staging init
container and fixed candidate, candidate-artifact, policy, reference-directory
and output paths. Structured
Job templates are deployment inputs rather than environment-variable values.

Construction validates every dependency and template but does not resolve a
plan, read a baseline, access object storage or call Kubernetes. During
resolution, immutable artifact IDs, URIs, checksums, sizes and destination paths
are bound to reserved environment variables on the input-staging init container.
Its image, command, volumes, endpoints and credential sources remain part of the
reviewed deployment template and production composition. Reporting currently
binds this staging contract for the normalized candidate and the exact policy
identity, version, SHA-256 digest and destination. Policy content is not embedded
in the Job. Available baseline artifacts are bound once per artifact ID with
indexed immutable metadata and deterministic result and artifact-reference paths.
A count variable defines the complete set; unavailable baselines are omitted.

## Reconciliation stages

Each lifecycle stage has an explicit deadline or rediscovery interval:

| Variable | Meaning |
| --- | --- |
| `PERFENG_RECONCILIATION_VALIDATION_ATTEMPT_TIMEOUT` | Execution-plan validation deadline |
| `PERFENG_RECONCILIATION_PROVISIONING_ATTEMPT_TIMEOUT` | Resolution, lease renewal, dispatch and binding deadline |
| `PERFENG_RECONCILIATION_EXECUTION_RETRY_AFTER` | Delay before observing a nonterminal execution again |
| `PERFENG_RECONCILIATION_CANCELLATION_POLL_EVERY` | Interval between stop-progress checks within an owned attempt |
| `PERFENG_RECONCILIATION_COLLECTION_ATTEMPT_TIMEOUT` | Raw-artifact verification deadline |
| `PERFENG_RECONCILIATION_COLLECTION_RETRY_AFTER` | Delay while raw artifacts are not yet visible |
| `PERFENG_RECONCILIATION_ANALYSIS_ATTEMPT_TIMEOUT` | Normalization reconciliation deadline |
| `PERFENG_RECONCILIATION_ANALYSIS_RETRY_AFTER` | Delay while normalization remains active |
| `PERFENG_RECONCILIATION_REPORTING_ATTEMPT_TIMEOUT` | Report reconciliation deadline |
| `PERFENG_RECONCILIATION_REPORTING_RETRY_AFTER` | Delay while report generation remains active |

Attempt deadlines must be positive and no greater than five minutes. The
provisioning deadline must additionally be no greater than half the worker
lease. Retry delays are whole-second values from one second through five
minutes. Cancellation polling is positive and no greater than five minutes.

`LoadReconciliationConfig` requires an already validated `worker.Config`, uses
that worker's lease for provisioning and returns the existing stage config
types. `ReconciliationConfig.Validate` rejects a later lease mismatch so runtime
composition cannot silently use different ownership and provisioning policy.
Loading policy does not create reconcilers, call external services or start the
worker.

`NewReconciliationWorker` is the inert lifecycle composition boundary. It
requires the durable reconciliation, execution and artifact store; the approved
Job resolver; constructed Kubernetes controllers, artifact collectors and
analysis/reporting executors; the event reporter; and both validated timing
configurations. It connects CREATED through REPORTING to the existing router and
bounded worker without reading PostgreSQL or object storage and without making a
Kubernetes request. The future executable remains responsible for constructing
those dependencies, running the worker, reporting safe operational events and
coordinating shutdown with the HTTP server.
