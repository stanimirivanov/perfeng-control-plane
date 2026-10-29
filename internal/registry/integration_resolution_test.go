package registry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationAdmissionResolutionPreservesOwnedReviewedValues(t *testing.T) {
	t.Parallel()
	entry, current, claims, record := integrationEntry(t)
	record.Qualification.Reasons = []string{}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	claims.Baseline.SHA256 = sha256Bytes(append(encoded, '\n'))
	registry := integrationRegistry(t, entry)
	for range 4 {
		t.Run("owned", func(t *testing.T) {
			t.Parallel()
			reads := 0
			read := func(_ context.Context, principal, id, version string) (baseline.Record, error) {
				reads++
				if principal != "alice" || id != record.ID || version != record.Version {
					t.Fatal("unscoped baseline read")
				}

				return record, nil
			}
			resolved, err := registry.ResolveIntegrationAdmission(context.Background(), "alice", claims, read)
			if err != nil || resolved.Request != current.Request || resolved.Target != run.ExecutionTargetKubernetes ||
				resolved.Workload != entry.Workload || !reflect.DeepEqual(resolved.Baseline, &record) || reads != 1 {
				t.Fatal("lost reviewed authority", err)
			}
			retained, err := json.Marshal(resolved.Baseline)
			if err != nil || sha256Bytes(append(retained, '\n')) != claims.Baseline.SHA256 {
				t.Fatal("owned snapshot changed the reviewed content pin", err)
			}
			resolved.Baseline.Lifecycle[0].Reason = "mutated"
			*resolved.Baseline.Dataset.Seed = 999
			*resolved.Baseline.Qualification.SampleCount = 999
			*resolved.Baseline.Qualification.MaximumCV = 999
			again, err := registry.ResolveIntegrationAdmission(context.Background(), "alice", claims, read)
			if err != nil || !reflect.DeepEqual(again.Baseline, &record) || reads != 2 {
				t.Fatal("returned baseline aliases review authority", err)
			}
		})
	}
}

func TestIntegrationAdmissionResolutionNeverReturnsPartialAuthority(t *testing.T) {
	for _, mode := range []string{"principal", "pin", "stale", "missing", "unavailable", "ambiguous", "cancelled", "cancelled during read"} {
		t.Run(mode, func(t *testing.T) {
			entry, _, claims, record := integrationEntry(t)
			registry := integrationRegistry(t, entry)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := run.ErrForbidden
			read := func(context.Context, string, string, string) (baseline.Record, error) {
				switch mode {
				case "stale":
					record.State = baseline.StateRetired
				case "missing":
					return baseline.Record{}, run.ErrNotFound
				case "unavailable":
					return baseline.Record{}, errors.New("private backend details")
				case "cancelled during read":
					cancel()
				}

				return record, nil
			}
			switch mode {
			case "principal":
				claims.Principal.ID = "bob"
			case "pin":
				claims.Baseline.SHA256 = strings.Repeat("f", 64)
			case "unavailable":
				want = run.ErrUnavailable
			case "ambiguous":
				second := entry
				second.Catalogue.ID += "-other"
				registry = integrationRegistry(t, entry, second)
			case "cancelled", "cancelled during read":
				want = context.Canceled
				if mode == "cancelled" {
					cancel()
				}
			}
			got, err := registry.ResolveIntegrationAdmission(ctx, "alice", claims, read)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, integration.AdmissionResolution{}) {
				t.Fatal("invalid review exposed partial authority", err)
			}
		})
	}
}

func TestIntegrationAdmissionResolutionSupportsSLOOnlyWithoutBaselineRead(t *testing.T) {
	entry, current, claims, _ := integrationEntry(t)
	var policy map[string]any
	if err := json.Unmarshal(entry.PolicyBytes, &policy); err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy["spec"].(map[string]any)["rules"].([]any) {
		delete(rule.(map[string]any), "regression")
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	entry.PolicyBytes, claims.Policy.SHA256, claims.Baseline = encoded, sha256Bytes(encoded), nil
	registry := integrationRegistry(t, entry)
	resolved, err := registry.ResolveIntegrationAdmission(context.Background(), "alice", claims, func(context.Context, string, string, string) (baseline.Record, error) {
		t.Fatal("SLO-only review read a baseline")
		return baseline.Record{}, run.ErrUnavailable
	})
	current.Request.Policy.SHA256 = claims.Policy.SHA256
	if err != nil || resolved.Baseline != nil || resolved.Request != current.Request || resolved.Workload != entry.Workload {
		t.Fatal("SLO-only resolution lost native values", err)
	}
	legacy, err := registry.ResolveIntegration(context.Background(), "alice", claims, nil)
	if err != nil || legacy != resolved.Request {
		t.Fatal("legacy request resolver changed", err)
	}
}
