package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// CatalogConfig opts a reviewed registry into the shared metadata projection.
type CatalogConfig struct {
	Issuer          string `json:"issuer"`
	PrincipalIssuer string `json:"principalIssuer"`
	ProducerVersion string `json:"producerVersion"`
}

func (config CatalogConfig) valid() bool {
	for _, raw := range []string{config.Issuer, config.PrincipalIssuer} {
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(raw) > 512 {
			return false
		}
	}
	return contract.ValidVersion(config.ProducerVersion)
}

func catalogReference(issuer, id, version, digest string) contract.CatalogReference {
	return contract.CatalogReference{CatalogIdentity: contract.CatalogIdentity{Issuer: issuer, ID: id}, Revision: version, SHA256: digest}
}

func catalogResource(kind, state string, reference contract.CatalogReference) map[string]any {
	return map[string]any{"kind": kind, "state": state, "reference": reference, "displayName": reference.ID}
}

func ownerResource(owner contract.CatalogOwnerRoute) map[string]any {
	resource := catalogResource("owner-route", "ACTIVE", owner.Reference)
	resource["owner"], resource["route"] = owner.Owner, owner.Route
	return resource
}

// PerformanceCatalog projects only opted-in entries visible to principal. Baseline
// reads are principal-scoped by the caller and never use producer-claimed authority.
// Existing per-entry evidence contract versions are neither changed nor inferred.
func (registry *ReportPolicyRegistry) PerformanceCatalog(ctx context.Context, principal string,
	read func(context.Context, string, string) (baseline.Record, error),
) (contract.CatalogProjection, error) {
	if err := ctx.Err(); err != nil {
		return contract.CatalogProjection{}, err
	}
	if registry.catalog == nil {
		return contract.CatalogProjection{}, run.ErrNotFound
	}
	if read == nil {
		return contract.CatalogProjection{}, run.ErrValidation
	}
	config := *registry.catalog
	projection := contract.CatalogProjection{Issuer: config.Issuer, PrincipalIssuer: config.PrincipalIssuer, ProducerVersion: config.ProducerVersion, Items: make([]map[string]any, 0)}
	resources := map[string]map[string]any{}
	put := func(resource map[string]any) error {
		if contract.ValidateCatalogResource(resource) != nil {
			return run.ErrUnavailable
		}
		ref := resource["reference"].(contract.CatalogReference)
		key := fmt.Sprintf("%s\x00%s\x00%s", resource["kind"], ref.Issuer, ref.ID)
		if previous, found := resources[key]; found && resource["kind"] == "profile" && reflect.DeepEqual(previous["reference"], resource["reference"]) && reflect.DeepEqual(previous["workload"], resource["workload"]) && previous["purpose"] == resource["purpose"] {
			merged := previous["selections"].([]map[string]any)
			for _, selection := range resource["selections"].([]map[string]any) {
				found := false
				for _, existing := range merged {
					if reflect.DeepEqual(existing, selection) {
						found = true
					}
				}
				if !found {
					merged = append(merged, selection)
				}
			}
			previous["selections"] = merged
			return nil
		}
		if previous, found := resources[key]; found && !reflect.DeepEqual(previous, resource) {
			return run.ErrUnavailable
		}
		resources[key] = resource
		return nil
	}
	for key, entry := range registry.entries {
		if err := ctx.Err(); err != nil {
			return projection, err
		}
		if key.principal != principal || entry.catalogOwner == nil {
			continue
		}
		switch key.profile {
		case "smoke", "average", "regression", "stress", "capacity", "soak":
		default:
			continue
		}
		// A request selects one baseline. Policies requiring several cannot be advertised as alternatives.
		if len(entry.baselines) > 1 {
			continue
		}
		workload := catalogReference(config.Issuer, entry.workload.ID, entry.workload.Version, entry.workload.SHA256)
		profile := workload // Native workloads already identify one immutable profile definition.
		environment := catalogReference(config.Issuer, key.environment.ID, key.environment.Version, key.environment.SHA256)
		policy := catalogReference(config.Issuer, key.policy.ID, key.policy.Version, key.policy.SHA256)
		workloadResource := catalogResource("workload", "ACTIVE", workload)
		workloadResource["ownerRoute"], workloadResource["supportedProfiles"] = entry.catalogOwner.Reference, []contract.CatalogReference{profile}
		profileResource := catalogResource("profile", "ACTIVE", profile)
		profileResource["purpose"], profileResource["workload"] = key.profile, workload
		selections := make([]map[string]any, 0)
		selection := map[string]any{"environment": environment, "policy": policy, "baseline": nil}
		eligible := true
		if len(entry.baselines) == 1 {
			expected := entry.baselines[0]
			record, err := read(ctx, expected.ID, expected.Version)
			if err != nil && !errors.Is(err, run.ErrNotFound) {
				return projection, err
			}
			eligible = err == nil && record.State == baseline.StateApproved
			if err == nil {
				if record.ID != expected.ID || record.Version != expected.Version || record.TestID != key.testID || record.Workload != entry.workload || record.Environment != expected.Environment || !reflect.DeepEqual(record.Dataset, expected.Dataset) || record.Validate() != nil {
					return projection, run.ErrUnavailable
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					return projection, run.ErrUnavailable
				}
				digest := sha256Bytes(append(encoded, '\n'))
				ref := catalogReference(config.Issuer, record.ID, record.Version+":"+strconv.FormatInt(record.Revision, 10), digest)
				resource := catalogResource("baseline", string(record.State), ref)
				resource["workload"], resource["profile"], resource["environment"], resource["policy"] = workload, profile, environment, policy
				if err := put(resource); err != nil {
					return projection, err
				}
				selection["baseline"] = ref
			}
		}
		if eligible {
			selections = append(selections, selection)
		}
		profileResource["selections"] = selections
		for _, resource := range []map[string]any{ownerResource(*entry.catalogOwner), workloadResource, profileResource, catalogResource("environment", "ACTIVE", environment), catalogResource("policy", "ACTIVE", policy)} {
			if err := put(resource); err != nil {
				return projection, err
			}
		}
	}
	keys := make([]string, 0, len(resources))
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if selections, ok := resources[key]["selections"].([]map[string]any); ok {
			sort.Slice(selections, func(left, right int) bool {
				a, _ := json.Marshal(selections[left])
				b, _ := json.Marshal(selections[right])
				return string(a) < string(b)
			})
		}
		projection.Items = append(projection.Items, resources[key])
	}
	return projection, nil
}

func sha256Bytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
