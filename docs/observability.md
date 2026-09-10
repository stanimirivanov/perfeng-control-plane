# Service observability

The control-plane HTTP listener exposes three unauthenticated operational
endpoints for in-cluster probes and scraping:

| Path | Meaning |
| --- | --- |
| `GET /livez` | The process HTTP handler is responsive |
| `GET /readyz` | Startup configuration and dependencies were acquired and the service is accepting requests |
| `GET /metrics` | Prometheus text exposition for bounded service metrics |

Other methods return 405 with `Allow: GET`. Operational responses disable
caching and content-type sniffing. The service is constructed only after the
database, authentication document, Kubernetes client, artifact storage and
reviewed registry have been loaded successfully, so an accessible readiness
endpoint represents successful startup. Ongoing deep dependency checks are not
performed; transient dependency behavior remains visible through API status
classes and reconciliation logs.

The initial metric set contains readiness, in-flight API requests, completed
requests by the fixed method set `GET`, `POST`, `OTHER` and bounded status class,
plus a request-duration histogram. Health checks and metric scrapes are excluded
from API request measurements. Labels never contain paths, Run IDs, principals,
tokens, request IDs, backend messages or arbitrary HTTP methods.

These process-local metrics reset on restart and are not evidence artifacts.
They provide deployment diagnostics only. Run-window telemetry correlation,
durable metric storage, dashboards, alerting and SUT observability remain later
roadmap phases.
