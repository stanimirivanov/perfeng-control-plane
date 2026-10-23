package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestPerformanceCatalogLoadsReviewedMetadataAndMergesSelections(t *testing.T) {
	source, _, _ := registryBundleFixture(t)
	var bundle reportPolicyBundle
	if err := json.Unmarshal(source[registryManifestPath].Data, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.PerformanceCatalog = &CatalogConfig{Issuer: "https://provider.example", PrincipalIssuer: "https://auth.example", ProducerVersion: "1.0.0"}
	bundle.Entries[0].CatalogOwnerRoute = &contract.CatalogOwnerRoute{Reference: catalogReference("https://provider.example", "owner", "1", strings.Repeat("a", 64)), Owner: contract.CatalogIdentity{Issuer: "https://owners.example", ID: "team"}, Route: contract.CatalogIdentity{Issuer: "https://routes.example", ID: "maintenance"}}
	// SLO-only profiles may be reviewed for several environments without inventing identities.
	var policy map[string]any
	if err := json.Unmarshal(source[registryPolicyPath].Data, &policy); err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy["spec"].(map[string]any)["rules"].([]any) {
		delete(rule.(map[string]any), "regression")
	}
	encoded, _ := json.Marshal(policy)
	source[registryPolicyPath] = &fstest.MapFile{Data: encoded}
	second := bundle.Entries[0]
	second.Environment.ID = "second-environment"
	second.ContractsVersion = "0.22.0"
	bundle.Entries = append(bundle.Entries, second)
	encoded, _ = json.Marshal(bundle)
	source[registryManifestPath] = &fstest.MapFile{Data: encoded}
	registry, err := LoadReportPolicyRegistry(source, registryManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := registry.PerformanceCatalog(context.Background(), "alice", func(context.Context, string, string) (baseline.Record, error) {
		t.Fatal("SLO-only profile read a baseline")
		return baseline.Record{}, run.ErrUnavailable
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogSelection(t, projection, 2)
	versions := map[string]bool{}
	for _, entry := range registry.entries {
		versions[entry.contractsVersion] = true
	}
	if len(versions) != 2 {
		t.Fatal("collapsed per-entry contract versions")
	}
	bundle.PerformanceCatalog.Issuer = "https://user:secret@provider.example"
	encoded, _ = json.Marshal(bundle)
	source[registryManifestPath] = &fstest.MapFile{Data: encoded}
	if _, err := LoadReportPolicyRegistry(source, registryManifestPath); !errors.Is(err, run.ErrValidation) {
		t.Fatal("accepted credential-bearing issuer")
	}
}

func TestPerformanceCatalogBaselineRetirementAndPrincipalIsolation(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	entry.CatalogOwnerRoute = &contract.CatalogOwnerRoute{Reference: catalogReference("https://provider.example", "search-owner", "1", strings.Repeat("a", 64)), Owner: contract.CatalogIdentity{Issuer: "https://owners.example", ID: "search-team"}, Route: contract.CatalogIdentity{Issuer: "https://routes.example", ID: "maintenance"}}
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.PerformanceCatalog(context.Background(), "alice", nil); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("legacy registry enabled publication")
	}
	registry.catalog = &CatalogConfig{Issuer: "https://provider.example", PrincipalIssuer: "https://auth.example", ProducerVersion: "1.0.0"}
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	record, err := baseline.New(baseline.Create{ID: "approved-search-browser", Version: "1.0.0", TestID: entry.TestID, SourceRunID: current.ID, Artifact: input.Candidate, Software: baseline.Software{GitSHA: current.Request.Candidate.GitSHA, Image: current.Request.Candidate.Image}, Workload: entry.Workload, Environment: entry.Environment, Dataset: entry.Dataset, Actor: "alice", Reason: "reviewed evidence"}, now)
	if err != nil {
		t.Fatal(err)
	}
	samples, cv := int64(5), 0.01
	record, err = record.Transition(record.Revision, baseline.Change{State: baseline.StateQualified, Actor: "reviewer", Reason: "qualified", Qualification: &baseline.Qualification{Status: baseline.QualificationPassed, SampleCount: &samples, MaximumCV: &cv, Reasons: []string{}}}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record, err = record.Transition(record.Revision, baseline.Change{State: baseline.StateApproved, Actor: "reviewer", Reason: "approved"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	read := func(context.Context, string, string) (baseline.Record, error) { return record, nil }
	missing, err := registry.PerformanceCatalog(context.Background(), "alice", func(context.Context, string, string) (baseline.Record, error) {
		return baseline.Record{}, run.ErrNotFound
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogSelection(t, missing, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.PerformanceCatalog(ctx, "bob", read); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation for an empty view")
	}
	for _, principal := range []string{"alice", "bob"} {
		projection, err := registry.PerformanceCatalog(context.Background(), principal, read)
		if err != nil {
			t.Fatal(err)
		}
		if principal == "bob" && len(projection.Items) != 0 {
			t.Fatal("cross-principal metadata leaked")
		}
		if principal == "alice" {
			assertCatalogSelection(t, projection, 1)
			encoded, _ := json.Marshal(projection.Items)
			for _, forbidden := range []string{"candidateImages", "executionTemplate", "s3://", "qualification", "secret"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("private field %s", forbidden)
				}
			}
		}
	}
	record, err = record.Transition(record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "reviewer", Reason: "retired"}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	projection, err := registry.PerformanceCatalog(context.Background(), "alice", read)
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogSelection(t, projection, 0)
	retired := false
	for _, resource := range projection.Items {
		retired = retired || resource["kind"] == "baseline" && resource["state"] == "RETIRED"
	}
	if !retired {
		t.Fatal("retired baseline lifecycle metadata disappeared")
	}
	_, err = registry.PerformanceCatalog(context.Background(), "alice", func(context.Context, string, string) (baseline.Record, error) {
		return baseline.Record{}, run.ErrUnavailable
	})
	if !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("lost baseline read failure")
	}
	for _, value := range registry.entries {
		if value.contractsVersion != entry.ContractsVersion {
			t.Fatal("entry version changed")
		}
	}
	entry.Profile = "unreviewed-profile"
	unsupported, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	unsupported.catalog = registry.catalog
	projection, err = unsupported.PerformanceCatalog(context.Background(), "alice", read)
	if err != nil || len(projection.Items) != 0 {
		t.Fatal("unsupported profile advertised")
	}
}

func assertCatalogSelection(t *testing.T, projection contract.CatalogProjection, count int) {
	t.Helper()
	found := false
	for _, resource := range projection.Items {
		if resource["kind"] == "profile" {
			found = true
			if len(resource["selections"].([]map[string]any)) != count {
				t.Fatal("incorrect baseline eligibility")
			}
		}
	}
	if !found {
		t.Fatal("missing profile")
	}
}
