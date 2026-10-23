# Authorized performance catalog

## TL;DR

- `GET /v1/performance-catalog` publishes safe metadata using contracts 0.24.0.
- Publication requires reviewed registry metadata and explicit `catalog:read` permission.
- Pages belong to one immutable, principal-scoped snapshot; restart enumeration on 410.
- Catalog availability never grants execution authority or overrides live baseline admission.

## Opt-in configuration

Deploy a compatible binary before extending the mounted registry or credentials;
older binaries strictly reject unknown configuration. No new environment variable
or database migration is required. Add this optional top-level registry member:

```json
"performanceCatalog": {
  "issuer": "https://provider.example/identity",
  "principalIssuer": "https://auth.example/tenant",
  "producerVersion": "1.0.0"
}
```

The issuer establishes the reviewed namespace for existing native resource IDs;
it is not inferred from request host names. The principal issuer scopes identities
already verified by the configured authenticator. The producer version identifies
the deployed producer release, not the evidence contracts version.
Issuer URIs must not contain user information, query parameters or fragments.

Each published registry entry also requires `catalogOwnerRoute`, containing
`reference: {issuer, id, revision, sha256}`, `owner: {issuer, id}` and
`route: {issuer, id}`. These are reviewed immutable artifact/routing references,
never destinations or fetched URLs. Entry metadata is copied at startup and its
shape/URI identity validated against the pinned contract. Operators must verify
the referenced artifact provenance when reviewing the bundle, just as they do
the existing workload/environment references; this endpoint cannot establish it.
Entries without owner metadata remain private. Removing the top-level member
disables publication. Old registry, authentication and evidence configuration
keeps its behavior and is not automatically opted in.

Grant `catalog:read` explicitly in the existing credential permission array.
Existing run/baseline permissions do not imply it; it grants no mutations or raw
baseline reads. Every page reauthenticates the bearer token, checks this permission
and matches the snapshot's principal. Clients cannot select another principal,
issuer, page size or registry binding through query parameters.

## Projection and baseline state

Only registry entries authorized for that principal are projected. Workload and
environment IDs, versions and hashes remain exact; native workloads already name
one immutable profile definition, so the same native identity is retained in the
contract's distinct workload/profile kinds. The native profile label supplies
purpose, not a fabricated identity. Environment/policy/baseline selections merge
only for identical profile identity, purpose and workload. Unknown purposes and
policies requiring several distinct baselines are omitted rather than advertised
as alternatives. Conflicting current resource identities fail publication closed.

Baseline reads use the authenticated principal and exact policy baseline version.
Records must match test/workload/environment/fingerprint/dataset and pass native
lifecycle validation. Their reference revision is `version:revision`; SHA-256
covers the exact JSON baseline GET body including its trailing newline. Only an
APPROVED compatible record enables its tuple. Missing or unapproved/retired
baselines leave that profile's selection empty, while visible baseline lifecycle
metadata remains available for review. Backend failures never become empty views.
Policies without a baseline explicitly publish a null baseline; this is not
implicit permission to bypass release policy.

Existing per-entry `contractsVersion` evidence selection is untouched. The catalog
schema pin coexists with the existing API/evidence snapshots; no artifact producer
is relabeled. Responses never include policy bodies, job templates, private
targets, candidate allowlists, raw artifacts or backend diagnostics.

## Pagination, recovery and limits

The first GET materializes the entire authorized metadata view. Subsequent GETs
use only `?cursor=<opaque value>` and return frozen pages; callers must verify
the complete snapshot before atomically replacing eligibility views. The digest
uses the contract's restricted canonical UTF-8 resource-array encoding, including
unescaped non-ASCII/HTML/line-separator characters and sorted ASCII object keys.
Cross-language fixtures verify compatibility.

Each page contains at most 100 resources. Snapshots expire after one minute and
their resource array is capped at 1 MiB. At most 16 snapshots/materializations are
retained per process, with a five-second source-read timeout. Capacity or source
failure returns a safe error, not partial success; 503 includes Retry-After.
Expired/unknown/cross-principal cursors return the same 410 response. Restarts
invalidate all ephemeral cursors. Restart enumeration on 410; reject incomplete
or mixed views. Old pages deliberately remain immutable during enumeration, so
fresh enumeration rereads retirement state and execution admission must always
revalidate the live approved baseline. Snapshots convey no lease or approval.

Caches are process-local. Multi-instance deployments must preserve instance
affinity throughout enumeration; another instance cannot resolve an existing
cursor and returns 410. Affinity or shared-cache deployment work is not included
in this binary change.

For binary rollback, first remove the top-level `performanceCatalog`, every
entry's `catalogOwnerRoute`, and all `catalog:read` grants, then revert the
additive binary with that compatible configuration. Registry and credential
changes take effect at process startup. No deployed workload, database state or existing run API changes.
See proposed [ADR-0010](decisions/0010-publish-authorized-performance-catalog.md).
