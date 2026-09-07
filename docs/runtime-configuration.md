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
| `PERFENG_ARTIFACT_BUCKET` | Approved artifact bucket |
| `PERFENG_RAW_MANIFEST_FORMAT` | Accepted raw manifest generation |
| `PERFENG_ARTIFACT_MAX_READ_BYTES` | Positive bounded object-read limit |

An absent endpoint selects normal AWS endpoint resolution and requires
`PERFENG_S3_ALLOW_HTTP=false`. A configured endpoint must contain only a scheme,
host and optional port. User information, paths, queries and fragments are
rejected. HTTPS is the default; HTTP is accepted only when the explicit flag is
true. This supports isolated local SeaweedFS without silently weakening a
production deployment.

Credentials are not part of this configuration value. The future AWS SDK client
must use its credential provider chain, populated by the runtime Secret or
workload identity. Configuration errors identify only the variable or policy
component and never echo configured values.

The loader produces `S3ClientConfig` together with the validated
`S3ArtifactAccessConfig`. It performs no network requests and creates no client.
HTTP server, PostgreSQL, Kubernetes, authentication and registry settings will
be added with the executable components that consume them.
