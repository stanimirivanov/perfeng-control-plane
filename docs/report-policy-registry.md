# Report-policy registry

`internal/registry.ReportPolicyRegistry` is the concrete in-process resolver for
report trust. A service constructs it from reviewed startup entries before
accepting work. The package intentionally defines no configuration file syntax,
network protocol, mutable administration API or background refresh mechanism.

Each entry binds exact policy bytes to:

- an explicit test ID;
- one catalogue ID, version and SHA-256 digest;
- one profile;
- one environment definition ID, version and digest plus its observed fingerprint;
- one workload identity and dataset identity;
- one digest-pinned report producer;
- one or more digest-pinned candidate images; and
- one or more authorized principals.

Policy identity and test identity are independent. The resolver never assumes
that `PerformancePolicy.metadata.name` equals the selected test ID. The policy
name, version and exact-byte digest form the policy reference, while the entry's
test ID supplies the separately reviewed applicability decision.

Construction strictly parses every policy and validates all entry identities.
Duplicate principal/context bindings reject the complete registry. Policy bytes,
baseline dataset seed pointers and returned selections are copied so callers
cannot mutate registry state. Once constructed, the registry performs read-only
map lookups and is safe for concurrent resolution.

Resolution requires an exact match on principal, test, catalogue, profile,
environment and policy. An unmatched valid context returns `run.ErrForbidden`
without revealing whether another principal has an entry. Invalid Runs,
non-REPORTING states and malformed candidate artifacts return
`run.ErrValidation`; context cancellation retains its identity.

`ApproveRun` has the `httpapi.Approve` signature and uses the same exact lookup.
It additionally requires the request's candidate image digest to appear in the
entry's allowlist. The candidate Git SHA remains part of the validated immutable
Run request, but this registry does not claim to attest the relationship between
that revision and the published image. An unmatched context or image is
forbidden; malformed input is a validation failure.

Report trust resolution does not reapply the candidate-image allowlist. The
candidate was authorized when the immutable Run was accepted; removing an image
from admission policy must not prevent that Run from completing reporting.
Principal and exact resource-context authorization are still resolved again.

The resolver derives baseline selections from the policy's distinct pinned
regression references and combines them with the reviewed workload, environment
and dataset compatibility dimensions. It does not query baseline storage,
choose latest versions or authorize evidence itself. `TrustedReportApprover`
performs exact baseline resolution and `policy.VerdictApprover` binds the result
back to each policy rule.

Production composition must define how reviewed entries are loaded, signed or
otherwise attested, refreshed and audited. Replacing the registry requires
constructing a new validated instance; partially mutating a live registry is not
supported.
