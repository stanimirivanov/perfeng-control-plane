# Runtime configuration

## TL;DR

- Startup policy is parsed and cross-validated before external resources are
  acquired.
- Mounted credentials, registries and Job templates are strict, bounded and
  validated without exposing sensitive values.
- PostgreSQL, Kubernetes and S3 clients are composed only after configuration
  succeeds; construction remains inert until runtime start.
- Configuration errors are value-free and fail closed before serving traffic or
  starting reconciliation.
- Deployment supplies credentials and reviewed inputs; this repository defines
  their runtime contract.

The future control-plane executable reads deployment configuration from its
process environment. Configuration parsing is separate from dependency
construction so malformed or contradictory policy fails before any network
client is created.

## Aggregate startup policy

`LoadApplicationConfig` reads every required environment setting into one typed
`ApplicationConfig`. It preserves the existing variable-specific, value-free
configuration errors and cross-validates the worker lease against reconciliation
policy. Loading does not open mounted files, connect to PostgreSQL, inspect the
AWS credential chain or construct a Kubernetes client. The executable can
therefore reject incomplete policy before it acquires resources; file-backed
content and external dependencies remain explicit later construction phases.

`NewStartupResources` performs the next bounded startup phase. It loads the
credential document, reviewed registry and executor templates before constructing
the inert S3 and in-cluster Kubernetes clients. PostgreSQL is opened last, after
every other fallible step, and is the only resource owned by the returned
`StartupResources`. Its concurrency-safe `Close` method releases that database
exactly once and reduces shutdown failures to a value-free classification.

`NewApplicationRuntime` is the inert service composition root. It connects the
acquired resources to Kubernetes controllers, verified evidence collectors,
analysis and reporting executors, the authenticated HTTP handler, structured
worker reporting, every lifecycle stage and the HTTP/worker supervisor. It does
not bind the listener, start a goroutine, query PostgreSQL, contact Kubernetes or
read object storage.

## Static bearer authentication

The first isolated local deployment uses a strict file-based credential
registry:

| Variable | Meaning |
| --- | --- |
| `PERFENG_AUTHENTICATION_FILE` | Canonical absolute path to the mounted credential document |

Credential content is never placed in an environment variable. The document
has this shape:

~~~json
{
  "schemaVersion": 1,
  "credentials": [
    {
      "tokenSha256": "<64 lowercase hexadecimal characters>",
      "principal": "local-operator",
      "permissions": ["runs:create", "runs:read"]
    }
  ],
  "nativeWorkers": [
    {
      "tokenSha256": "<64 lowercase hexadecimal characters>",
      "workerId": "windows-laptop-01",
      "authorizedToAcquire": true,
      "qualificationEvidence": {
        "environmentId": "windows-mainstream",
        "environmentSha256": "<reviewed SHA-256>",
        "runner": {
          "kind": "source-checkout",
          "repository": "https://github.com/example/perfeng-playwright.git",
          "revision": "<40 lowercase hexadecimal characters>",
          "lockfile": "pnpm-lock.yaml",
          "lockfileSha256": "<reviewed SHA-256>"
        }
      }
    }
  ]
}
~~~

The accepted permissions are `runs:create`, `runs:read`, `runs:cancel`,
`baselines:create`, `baselines:read` and `baselines:transition`. The loader
rejects unknown or duplicate fields, digests and permissions. It
also rejects two credentials for the same principal when their permissions
differ. Consequently, overlapping old and new token hashes can rotate a
credential without changing the durable principal or its authorization.

`nativeWorkers` is optional until a native agent is provisioned. Its tokens are
an independent authentication audience: user credentials cannot call the Native
Worker API, and worker credentials cannot call Run or baseline administration.
The request cannot select `workerId`, qualification or acquisition authorization.
The server compares reported evidence with the exact reviewed evidence above;
availability, qualification and authorization remain separate values. Repeated
entries may overlap tokens during rotation only when their worker identity and
policy are identical.

`POST /native-worker/v1/presence` records the most recent authenticated report
using PostgreSQL time. The initial policy requests another heartbeat after 30
seconds and qualifies matching evidence for two minutes. These values are
server-owned; the worker cannot extend them.

`POST /native-worker/v1/leases` accepts the pinned protocol version and a
bounded `maxWaitSeconds` hint. This initial implementation responds immediately:
`200` returns one compatible durable lease and places its one-time plaintext
capability only in `X-Perfeng-Lease`; `204` means no eligible work is currently
available and requests a retry after 15 seconds. True bounded long polling is a
later worker-efficiency decision, so clients must not assume the server waits
for the requested maximum. Acquisition requires current AVAILABLE presence,
matching reviewed qualification evidence, explicit acquisition authorization,
compatible capabilities and free worker capacity. The initial server-owned
policy uses a two-minute presence window, two-minute lease, 40-second renewal
interval and a bounded batch of 100 candidate Runs.

Only SHA-256 token digests are read and retained. The bearer tokens themselves
must be generated with sufficient entropy and delivered separately to clients;
they do not belong in this document, process arguments, ordinary logs or the
repository. The future executable will mount the document from a Kubernetes
Secret and supply its path explicitly. Startup reads the complete bounded file
once and composes the resulting immutable authenticator with the HTTP handler.

This adapter is intended for the isolated local vertical slice. It does not
provide token issuance, revocation, expiry, rate limiting or federated identity.
TLS is still required when traffic leaves the isolated development environment.
A production identity provider and its stable-principal mapping remain a
separate deployment decision.

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
version. The registry is loaded from two explicit settings:

| Variable | Meaning |
| --- | --- |
| `PERFENG_REGISTRY_ROOT` | Canonical absolute non-root directory of the mounted reviewed bundle |
| `PERFENG_REGISTRY_MANIFEST` | Portable manifest path relative to that root |

Startup strictly loads the complete immutable registry and rejects it unless
every entry uses `PERFENG_CONTRACTS_VERSION`. Registry paths and content are not
included in returned configuration errors. Signing, attestation and refresh
policy remain deployment responsibilities.

A source-checkout or release-archive entry must include `nativeAssignment`:

~~~json
{
  "baseUrl": "https://sut.perfeng.invalid/search",
  "browser": "edge",
  "diagnosticMode": "trace",
  "artifactPlan": [
    {
      "slot": "measurements",
      "kind": "measurements",
      "mediaType": "application/json",
      "format": "playwright-measurements-v2",
      "maximumBytes": 1048576,
      "required": true
    },
    {
      "slot": "manifest",
      "kind": "manifest",
      "mediaType": "application/json",
      "format": "raw-result-v2",
      "maximumBytes": 1048576,
      "required": true
    }
  ]
}
~~~

The registry derives test, workload, profile, environment and execution-artifact
identity from the already reviewed entry. Native Worker API v1 accepts only the
Playwright producer. The registry also rejects a native assignment on a
Kubernetes entry, a missing assignment on native provenance, non-HTTPS targets,
unsupported browsers or diagnostics, duplicate slots and unbounded artifacts.
This configuration is resolved before native lease acquisition; workers and Run
callers cannot supply or override it.

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
and output paths. Structured Job templates are deployment inputs selected
through two explicit settings:

| Variable | Meaning |
| --- | --- |
| `PERFENG_ANALYSIS_JOB_FILE` | Canonical absolute path to the reviewed normalization Job |
| `PERFENG_REPORT_JOB_FILE` | Canonical absolute path to the reviewed reporting Job |

Each file is bounded to 1 MiB and decoded as one strict YAML or JSON Job;
duplicate and unknown fields are rejected. The two paths must be distinct.
Container names and workspace paths are fixed by the control-plane staging
protocol rather than independently configurable deployment values.

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
Kubernetes request. `NewApplicationRuntime` supplies those dependencies from one
validated resource bundle and coordinates this worker with the HTTP server.

## Operational event reporting

`NewWorkerEventReporter` adapts worker failures to a concurrency-safe structured
logger. Each error-level record uses the fixed message
`reconciliation operation failed` and the fixed fields `component`, `operation`
and `error_class`. A `run_id` field is included only when it satisfies the Run
contract. Arbitrary error text, invalid operation values and malformed Run IDs
are never passed to the logger, preventing wrapped provider responses, database
details and credentials from becoming ordinary diagnostics.

Known domain failures receive stable classifications such as `unavailable`,
`lease_lost`, `job_conflict`, `artifacts_not_ready`, `analysis_failed` and
`report_failed`; unclassified errors use `unknown`. Logger handler, output
destination, retention and redaction of attributes added outside this adapter
remain executable and deployment policy.

`NewRuntime` supervises the constructed HTTP server and reconciliation worker as
one process lifetime. `Runtime.Run` starts both loops, cancels the peer when one
exits, and applies the configured shutdown timeout to HTTP shutdown and peer
termination.
Cancellation of the caller context is a successful graceful stop. Unexpected
serve, worker or shutdown errors return the redacted `ErrRuntimeFailure`
classification.

## Process entry point

`cmd/control-plane` completes the process lifecycle. It installs cancellation
for interrupt and termination signals, constructs a JSON logger on standard
error, loads configuration, acquires resources, composes the application, runs
the supervised services and closes resources after they stop. Signal-driven
cancellation is successful; startup, runtime and cleanup failures return a
non-zero status.

Process records use fixed messages and the stable fields `component`, `phase`
and, for failures, `error_class`. Returned error text is never logged. Known
configuration, dependency, runtime and resource-shutdown sentinels receive fixed
classifications; all other errors become `unknown`.
