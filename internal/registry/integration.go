package registry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// IntegrationBinding is an operator-reviewed association between external pins
// and this entry's candidate. References are immutable claims whose provenance
// must be verified during bundle review; the registry never fetches them.
type IntegrationBinding struct {
	RequestIssuer string                      `json:"requestIssuer"`
	Service       contract.CatalogReference   `json:"service"`
	Capability    contract.CatalogReference   `json:"capability"`
	Deployment    contract.CatalogReference   `json:"deployment"`
	Candidate     run.Candidate               `json:"candidate"`
	Evidence      []contract.CatalogReference `json:"evidence"`
}

// IntegrationNamespaces returns reviewed immutable provider/principal namespaces.
// Enabled requires at least one opt-in binding; no I/O or caller claims are used.
func (registry *ReportPolicyRegistry) IntegrationNamespaces() (provider, principalIssuer string, enabled bool) {
	if registry == nil || registry.catalog == nil {
		return "", "", false
	}
	for _, entry := range registry.entries {
		if entry.integration != nil {
			return registry.catalog.Issuer, registry.catalog.PrincipalIssuer, true
		}
	}
	return "", "", false
}

func prepareIntegration(binding *IntegrationBinding, request run.Request) (*IntegrationBinding, error) {
	if binding == nil {
		return nil, nil
	}
	request.Candidate = binding.Candidate
	if request.Validate() != nil || !(CatalogConfig{Issuer: binding.RequestIssuer, PrincipalIssuer: binding.RequestIssuer, ProducerVersion: "1.0.0"}).valid() ||
		binding.Evidence == nil || len(binding.Evidence) > 32 {
		return nil, run.ErrValidation
	}
	seen := map[contract.CatalogIdentity]bool{}
	for _, ref := range append([]contract.CatalogReference{binding.Service, binding.Capability, binding.Deployment}, binding.Evidence...) {
		if contract.ValidateCatalogResource(catalogResource("policy", "ACTIVE", ref)) != nil {
			return nil, run.ErrValidation
		}
	}
	for _, ref := range binding.Evidence {
		if seen[ref.CatalogIdentity] {
			return nil, run.ErrValidation
		}
		seen[ref.CatalogIdentity] = true
	}
	copy := *binding
	copy.Evidence = append([]contract.CatalogReference{}, binding.Evidence...)

	return &copy, nil
}

// ResolveIntegration maps an already wire-validated context to one reviewed Run
// request. principal must come from authentication, never the payload. read must
// honor its explicit principal and return a fresh baseline snapshot. This method
// is a concurrent immutable lookup, with optional baseline I/O outside transactions;
// it neither accepts work nor enforces admission deadlines. Failures return no
// partial request. Cancellation is preserved and backend diagnostics are hidden.
func (registry *ReportPolicyRegistry) ResolveIntegration(ctx context.Context, principal string, claims contract.IntegrationContext,
	read func(context.Context, string, string, string) (baseline.Record, error),
) (run.Request, error) {
	resolved, err := registry.ResolveIntegrationAdmission(ctx, principal, claims, read)

	return resolved.Request, err
}

// ResolveIntegrationAdmission returns owned native values from the same unique
// reviewed entry and principal-scoped baseline read as ResolveIntegration. It
// preserves cancellation and returns zero output on error. Calls are concurrent-
// safe; the only I/O is through read, which must return an owned baseline value.
// Claims require prior wire validation and principal independent authentication.
// Retention consumers must hold its approval lock through their admission commit;
// this method performs no persistence, clock lookup or historical reconstruction.
func (registry *ReportPolicyRegistry) ResolveIntegrationAdmission(ctx context.Context, principal string, claims contract.IntegrationContext,
	read func(context.Context, string, string, string) (baseline.Record, error),
) (integration.AdmissionResolution, error) {
	if err := ctx.Err(); err != nil {
		return integration.AdmissionResolution{}, err
	}
	if registry == nil || registry.catalog == nil || principal == "" ||
		claims.Principal != (contract.CatalogIdentity{Issuer: registry.catalog.PrincipalIssuer, ID: principal}) {
		return integration.AdmissionResolution{}, run.ErrForbidden
	}
	var selected reportPolicyValue
	var request run.Request
	matches := 0
	for key, entry := range registry.entries {
		binding := entry.integration
		if key.principal != principal || binding == nil || !binding.matches(claims) {
			continue
		}
		issuer := registry.catalog.Issuer
		workload := catalogReference(issuer, entry.workload.ID, entry.workload.Version, entry.workload.SHA256)
		if claims.Workload != workload || claims.Profile != workload ||
			claims.Environment != catalogReference(issuer, key.environment.ID, key.environment.Version, key.environment.SHA256) ||
			claims.Policy != catalogReference(issuer, key.policy.ID, key.policy.Version, key.policy.SHA256) {
			continue
		}
		matches++
		selected = entry
		request = run.Request{TestSuite: key.testID, Catalogue: key.catalogue, Profile: key.profile, Candidate: binding.Candidate, Environment: key.environment, Policy: key.policy}
	}
	if matches != 1 {
		return integration.AdmissionResolution{}, run.ErrForbidden
	}
	target, err := registry.ApproveRun(ctx, principal, request)
	if err != nil {
		return integration.AdmissionResolution{}, err
	}
	approved, err := integrationBaseline(ctx, principal, registry.catalog.Issuer, selected.baselines, claims.Baseline, read)
	if err != nil {
		return integration.AdmissionResolution{}, err
	}
	if err := ctx.Err(); err != nil {
		return integration.AdmissionResolution{}, err
	}

	return integration.AdmissionResolution{Request: request, Target: target, Workload: selected.workload, Baseline: approved}, nil
}

func (binding IntegrationBinding) matches(claims contract.IntegrationContext) bool {
	return claims.Request.Issuer == binding.RequestIssuer && claims.Service == binding.Service &&
		claims.Capability == binding.Capability && claims.Deployment == binding.Deployment &&
		claims.Candidate.Source.Algorithm == "git-sha1" && claims.Candidate.Source.Digest == binding.Candidate.GitSHA &&
		claims.Candidate.ImageDigest == "sha256:"+strings.SplitN(binding.Candidate.Image, "@sha256:", 2)[1] &&
		reflect.DeepEqual(claims.Evidence, binding.Evidence)
}

func integrationBaseline(ctx context.Context, principal, issuer string, selections []baseline.Selection, pin *contract.CatalogReference,
	read func(context.Context, string, string, string) (baseline.Record, error),
) (*baseline.Record, error) {
	if len(selections) == 0 && pin == nil {
		return nil, nil
	}
	if len(selections) != 1 || pin == nil || read == nil {
		return nil, run.ErrForbidden
	}
	expected := selections[0]
	if pin.Issuer != issuer || pin.ID != expected.ID {
		return nil, run.ErrForbidden
	}
	record, err := read(ctx, principal, expected.ID, expected.Version)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, run.ErrNotFound) {
		return nil, run.ErrForbidden
	}
	if err != nil {
		return nil, run.ErrUnavailable
	}
	if !record.MatchesSelection(expected) {
		return nil, run.ErrForbidden
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, run.ErrUnavailable
	}
	if *pin != catalogReference(issuer, record.ID, record.Version+":"+strconv.FormatInt(record.Revision, 10), sha256Bytes(append(encoded, '\n'))) {
		return nil, run.ErrForbidden
	}

	retained := record.Clone()
	// Empty and null reason arrays serialize differently under the approved pin.
	retained.Qualification.Reasons = slices.Clone(record.Qualification.Reasons)

	return &retained, nil
}
