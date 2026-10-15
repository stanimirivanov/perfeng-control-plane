# Report-policy registry

## TL;DR

- The registry is the immutable in-process authority for admission, execution,
  producer provenance and report trust.
- Entries bind exact policy, catalogue, environment, workload, producer,
  template, candidate-image and principal identities.
- Construction validates the whole registry before work is accepted; runtime
  resolution performs exact allowlist matching.
- The registry does not fetch remote configuration, refresh itself or expose a
  mutable administration API.
- Policy identity and test identity remain independent reviewed decisions.

`internal/registry.ReportPolicyRegistry` is the concrete in-process authority
for Run admission, execution templates, producer provenance and report trust. A
service constructs it from reviewed startup entries before accepting work. The
package intentionally defines no configuration file syntax, network protocol,
mutable administration API or background refresh mechanism.

Each entry binds exact policy bytes to:

- an explicit test ID;
- one catalogue ID, version and SHA-256 digest;
- one profile;
- one environment definition ID, version and digest plus its observed fingerprint;
- one workload identity and dataset identity;
- one contracts bundle version and raw producer, using either the v1
  digest-pinned image or the v2 OCI-image, source-checkout or release-archive
  identity;
- one digest-pinned normalizer producer;
- for container execution, one reusable execution Job template containing the
  raw-producer image;
- for native execution, one assignment template containing the reviewed HTTPS
  target, browser, diagnostic mode and bounded artifact slots;
- optionally, for a v2 baseline browser Run, a reviewed measurement recipe
  template with qualified Windows-host evidence and rendering settings;
- one digest-pinned report producer;
- one or more digest-pinned candidate images; and
- one or more authorized principals.

Policy identity and test identity are independent. The resolver never assumes
that `PerformancePolicy.metadata.name` equals the selected test ID. The policy
name, version and exact-byte digest form the policy reference, while the entry's
test ID supplies the separately reviewed applicability decision.

Construction strictly parses every policy and validates all entry identities.
Process composition may explicitly select registry-scoped contracts verification
with `PERFENG_CONTRACTS_VERSION=registry`; numeric settings retain the uniform
startup constraint. `ResolveContractsVersion` selects the authorized entry's
exact version for COLLECTING, ANALYZING and REPORTING before evidence I/O. It
uses the same exact principal/resource-context lookup as provenance approval,
not an artifact claim, mutable global version or candidate readmission. Invalid
context returns `run.ErrValidation`, an unmatched binding returns
`run.ErrForbidden`, and cancellation retains its identity. Concurrent lookups
perform no writes or external I/O. See [ADR-0008](decisions/0008-resolve-evidence-contracts-per-registry-entry.md).

V1 and v2 OCI producers require a reusable Job template that passes the
restricted Kubernetes policy and contains the configured producer image. V2
source checkouts and release archives require no Job template; supplying one is
rejected so native provenance cannot accidentally enter the Kubernetes
dispatcher. They instead require the Playwright producer and a valid native
assignment template; container entries reject one. Duplicate
principal/context bindings reject the complete registry. Policy bytes, producer
artifacts, Job templates, baseline dataset seed pointers and returned selections
are copied so callers cannot mutate registry state. Once constructed, the
registry performs read-only map lookups and is safe for concurrent resolution.

Resolution requires an exact match on principal, test, catalogue, profile,
environment and policy. An unmatched valid context returns `run.ErrForbidden`
without revealing whether another principal has an entry. Invalid Runs,
non-REPORTING states and malformed candidate artifacts return
`run.ErrValidation`; context cancellation retains its identity.

`ApproveRun` has the `httpapi.Approve` signature and uses the same exact lookup.
It additionally requires the request's candidate image digest to appear in the
entry's allowlist and returns the server-owned execution target. A validated
OCI producer with a reviewed Job template selects `KUBERNETES`; a validated
source-checkout or release-archive producer without a Job template selects
`NATIVE_WINDOWS`. Admission persists that decision outside the caller-controlled
Run snapshot. The candidate Git
SHA remains part of the validated immutable Run request, but this registry does
not claim to attest the relationship between that revision and the published
image. An unmatched context or image is forbidden; malformed input is a
validation failure.

A native entry using the Native Worker v2 baseline contract and a Playwright
source checkout declares `nativeBaseline`. Construction validates the
cache/reuse and iteration policy, qualified host profile
ID/version/fingerprint/evidence digest, browser viewport and scale, and the two
artifact limits and its HTTPS target URL.
`ResolveNativeBaselinePlan` then combines that template with the immutable Run,
workload and producer identities to form the complete Native Worker v2 baseline
recipe: Chromium, search journey, baseline mode, and ordered measurements/v2
and raw-result/v2 slots. The Run's environment ID identifies the SUT; the
recipe's qualified host profile identifies the Windows execution environment.
These are deliberately different identities. The result is an isolated copy,
validated against the pinned v2 lease contract in tests.

V2 baseline selection compares that reviewed plan with a contract-validated
worker report. It requires runner contract 2, Windows/Playwright capacity one,
the browser, baseline mode, execution transport and both artifact kinds. The
reported qualification evidence must identify the reviewed host profile and
evidence digest and the exact runner artifact revision and lockfile. A missing
v2 registry entry is skipped; a registry failure is returned. Selection is
advisory and does not establish freshness, worker authorization, Run ownership
or lease capacity. The v2 storage transaction rechecks these under locks. The
v2 HTTP lease route is present, but acquisition is disabled by default through
the explicit startup switch until the Windows worker is verified.

Plan resolution itself does not persist or issue a lease. The native lease
acquirer reads persisted eligibility inputs, resolves plans before opening a
transaction and asks storage to recheck the exact presence and Run under locks.
Report-policy resolution therefore remains free of persistence and lease
ownership concerns.

`ResolveJob` implements `reconcile.JobResolver` for VALIDATING and PROVISIONING
Runs. It resolves the same exact principal and request context, requires the
candidate image to remain authorized and returns a new deep copy of the reviewed
template on every call. Native source-checkout and release-archive entries are
forbidden rather than returning a synthetic Job. The dispatcher assigns Run
identity and namespace
after resolution. The registry does not accept a Job manifest from the Run API.

Report trust resolution does not reapply the candidate-image allowlist. The
candidate was authorized when the immutable Run was accepted; removing an image
from admission policy must not prevent that Run from completing reporting.
Principal and exact resource-context authorization are still resolved again.

`ApproveRawManifest` implements `reconcile.RawManifestApprover`. It accepts only
COLLECTING Runs, validates the complete raw manifest, resolves the same exact
principal and resource context, and requires the configured contracts version,
test, workload, schema generation and complete raw producer identity. V2
comparison includes the artifact kind and every OCI-image, native
source-checkout or release-archive field. A syntactically valid but different
claim is forbidden.
Malformed manifests and invalid reconciliation context are validation failures.
The method does not locate or read the manifest or any declared object.

`ApproveNormalizedManifest` implements
`reconcile.NormalizedManifestApprover`. It accepts only ANALYZING Runs, validates
the normalized envelope and raw evidence references, resolves the same exact
principal and resource context, and requires the configured contracts version,
test, workload, normalizer and complete source set. Dynamic measurement-window
and creation-order binding is performed by `VerifiedNormalizedCollector` against
the verified persisted raw manifest; the registry performs no object reads.

The resolver derives baseline selections from the policy's distinct pinned
regression references and combines them with the reviewed workload, environment
and dataset compatibility dimensions. It does not query baseline storage,
choose latest versions or authorize evidence itself. `TrustedReportApprover`
performs exact baseline resolution and `policy.VerdictApprover` binds the result
back to each policy rule.

## Reviewed file bundles

`LoadReportPolicyRegistry` constructs the authority from an injected `fs.FS` and
one relative manifest path. The manifest has `schemaVersion: 1` and a non-empty
`entries` array. Each entry contains the fields of `ReportPolicyEntry`, except:

- `policy` is a relative path to the exact policy JSON bytes;
- `executionTemplate` is an optional relative path to a Kubernetes Job JSON
  document; and
- `normalization` contains `sourceFormat` and `layout`. k6 summary sources require
  a reviewed typed or legacy-export layout; Playwright measurements/v2 and
  kube-burner pod-latency sources require an empty layout.

Native entries may include the `nativeBaseline` object described above. Entries
without it cannot admit new native Runs, but can still be parsed for existing
report-provenance checks.

The loader bounds the manifest to 4 MiB and each referenced policy or Job to
1 MiB. JSON must use UTF-8, unique keys, known fields and no more than the shared
nesting limit. Paths use portable forward-slash `io/fs` syntax and cannot be
absolute, empty, dot-relative, parent-relative or backslash-separated. The
injected filesystem remains responsible for enforcing its own root and symlink
trust boundary.

Policy files are not parsed and reserialized: their exact bytes enter the
registry so the accepted SHA-256 reference remains reproducible. Job documents
are decoded into typed Kubernetes objects. The existing registry constructor
then validates all identities, producer/runtime combinations, restricted Job
policy, duplicate bindings and authorization context, and copies all mutable
data. File reads happen only during construction.

Validate a mounted or checked-out bundle without starting the API, worker,
database, object-store client or Kubernetes client:

```sh
go run ./cmd/validate-registry \
  -root ./deployment/runtime-registry \
  -manifest registry.json
```

Success prints one confirmation and exits zero. Invalid arguments exit 2;
unreadable or invalid bundle content exits 1 with a generic message that does
not include configured paths, policy contents or parser details. Deployment
repositories can use the same command in CI before publishing configuration.

Production composition must still define how the bundle is signed or otherwise
attested, mounted, refreshed and audited. Replacing the registry requires
constructing a new validated instance; partially mutating a live registry is not
supported.
