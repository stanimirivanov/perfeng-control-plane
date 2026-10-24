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

func integrationEntry(t *testing.T) (ReportPolicyEntry, run.Run, contract.IntegrationContext, baseline.Record) {
	t.Helper()
	entry, current, input := reportPolicyFixture(t)
	ref := func(id string) contract.CatalogReference {
		return catalogReference("https://provider.example", id, "1", strings.Repeat("a", 64))
	}
	entry.CatalogOwnerRoute = &contract.CatalogOwnerRoute{Reference: ref("owner"), Owner: contract.CatalogIdentity{Issuer: "https://owners.example", ID: "team"}, Route: contract.CatalogIdentity{Issuer: "https://routes.example", ID: "route"}}
	entry.Integration = &IntegrationBinding{RequestIssuer: "https://argus.example", Service: ref("service"), Capability: ref("capability"), Deployment: ref("deployment"), Candidate: current.Request.Candidate, Evidence: []contract.CatalogReference{ref("evidence")}}
	claims := contract.IntegrationContext{Request: contract.CatalogIdentity{Issuer: entry.Integration.RequestIssuer, ID: "request"}, Principal: contract.CatalogIdentity{Issuer: "https://auth.example", ID: "alice"}, Service: ref("service"), Capability: ref("capability"), Deployment: ref("deployment"), Evidence: append([]contract.CatalogReference{}, entry.Integration.Evidence...)}
	claims.Workload = catalogReference("https://provider.example", entry.Workload.ID, entry.Workload.Version, entry.Workload.SHA256)
	claims.Profile = claims.Workload
	claims.Environment = catalogReference("https://provider.example", current.Request.Environment.ID, current.Request.Environment.Version, current.Request.Environment.SHA256)
	claims.Policy = catalogReference("https://provider.example", current.Request.Policy.ID, current.Request.Policy.Version, current.Request.Policy.SHA256)
	claims.Candidate.Source.Algorithm, claims.Candidate.Source.Digest = "git-sha1", current.Request.Candidate.GitSHA
	claims.Candidate.ImageDigest = "sha256:" + strings.Repeat("2", 64)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	record, err := baseline.New(baseline.Create{ID: "approved-search-browser", Version: "1.0.0", TestID: entry.TestID, SourceRunID: current.ID, Artifact: input.Candidate, Software: baseline.Software{GitSHA: current.Request.Candidate.GitSHA, Image: current.Request.Candidate.Image}, Workload: entry.Workload, Environment: entry.Environment, Dataset: entry.Dataset, Actor: "alice", Reason: "reviewed"}, now)
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
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	pin := catalogReference("https://provider.example", record.ID, "1.0.0:3", sha256Bytes(append(encoded, '\n')))
	claims.Baseline = &pin

	return entry, current, claims, record
}

func integrationRegistry(t *testing.T, entries ...ReportPolicyEntry) *ReportPolicyRegistry {
	t.Helper()
	registry, err := NewReportPolicyRegistry(entries)
	if err != nil {
		t.Fatal(err)
	}
	registry.catalog = &CatalogConfig{Issuer: "https://provider.example", PrincipalIssuer: "https://auth.example", ProducerVersion: "1.0.0"}

	return registry
}

func TestIntegrationResolvesExactReviewedRequestAndIsolatesAuthority(t *testing.T) {
	t.Parallel()
	entry, current, claims, record := integrationEntry(t)
	registry := integrationRegistry(t, entry)
	if provider, issuer, enabled := registry.IntegrationNamespaces(); !enabled || provider != "https://provider.example" || issuer != "https://auth.example" {
		t.Fatal("lost integration namespaces")
	}
	entry.Integration.Service.ID = "mutated"
	entry.Integration.Evidence[0].ID = "mutated"
	read := func(ctx context.Context, principal, id, version string) (baseline.Record, error) {
		if principal != "alice" || id != record.ID || version != record.Version {
			t.Fatal("unscoped baseline lookup")
		}
		return record.Clone(), nil
	}
	for i := 0; i < 4; i++ {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			got, err := registry.ResolveIntegration(context.Background(), "alice", claims, read)
			if err != nil || got != current.Request {
				t.Fatalf("resolution: %+v, %v", got, err)
			}
		})
	}
}

func TestIntegrationRejectsChangedPinsAndPrincipal(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*contract.IntegrationContext){
		"principal":        func(c *contract.IntegrationContext) { c.Principal.ID = "bob" },
		"principal issuer": func(c *contract.IntegrationContext) { c.Principal.Issuer += "/other" },
		"request issuer":   func(c *contract.IntegrationContext) { c.Request.Issuer += "/other" },
		"service":          func(c *contract.IntegrationContext) { c.Service.SHA256 = strings.Repeat("b", 64) },
		"capability":       func(c *contract.IntegrationContext) { c.Capability.Revision = "2" },
		"deployment":       func(c *contract.IntegrationContext) { c.Deployment.ID += "-other" },
		"workload":         func(c *contract.IntegrationContext) { c.Workload.Revision = "2" },
		"profile":          func(c *contract.IntegrationContext) { c.Profile.Issuer += "/other" },
		"environment":      func(c *contract.IntegrationContext) { c.Environment.ID += "-other" },
		"policy":           func(c *contract.IntegrationContext) { c.Policy.SHA256 = strings.Repeat("b", 64) },
		"source":           func(c *contract.IntegrationContext) { c.Candidate.Source.Digest = strings.Repeat("f", 40) },
		"source algorithm": func(c *contract.IntegrationContext) { c.Candidate.Source.Algorithm = "git-sha256" },
		"image":            func(c *contract.IntegrationContext) { c.Candidate.ImageDigest = "sha256:" + strings.Repeat("f", 64) },
		"evidence":         func(c *contract.IntegrationContext) { c.Evidence[0].SHA256 = strings.Repeat("b", 64) },
		"baseline":         func(c *contract.IntegrationContext) { c.Baseline.SHA256 = strings.Repeat("b", 64) },
		"no baseline":      func(c *contract.IntegrationContext) { c.Baseline = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			entry, _, claims, record := integrationEntry(t)
			registry := integrationRegistry(t, entry)
			mutate(&claims)
			got, err := registry.ResolveIntegration(context.Background(), "alice", claims, func(context.Context, string, string, string) (baseline.Record, error) { return record, nil })
			if !errors.Is(err, run.ErrForbidden) || got != (run.Request{}) {
				t.Fatalf("accepted altered context: %+v %v", got, err)
			}
		})
	}
}

func TestIntegrationRejectsAmbiguityLegacyAndLiveBaselineFailures(t *testing.T) {
	entry, _, claims, record := integrationEntry(t)
	read := func(context.Context, string, string, string) (baseline.Record, error) { return record, nil }
	second := entry
	second.Catalogue.ID += "-other"
	ambiguous := integrationRegistry(t, entry, second)
	if _, err := ambiguous.ResolveIntegration(context.Background(), "alice", claims, read); !errors.Is(err, run.ErrForbidden) {
		t.Fatal("ambiguous mapping accepted")
	}
	entry.Integration = nil
	legacy := integrationRegistry(t, entry)
	if _, _, enabled := legacy.IntegrationNamespaces(); enabled {
		t.Fatal("legacy registry enabled routes")
	}
	if _, err := legacy.ResolveIntegration(context.Background(), "alice", claims, read); !errors.Is(err, run.ErrForbidden) {
		t.Fatal("legacy entry opted in")
	}
	entry, _, claims, record = integrationEntry(t)
	registry := integrationRegistry(t, entry)
	for _, principal := range []string{"bob", ""} {
		if _, err := registry.ResolveIntegration(context.Background(), principal, claims, read); !errors.Is(err, run.ErrForbidden) {
			t.Fatal("principal bypass")
		}
	}
	for _, failure := range []error{run.ErrNotFound, errors.New("private backend detail")} {
		_, err := registry.ResolveIntegration(context.Background(), "alice", claims, func(context.Context, string, string, string) (baseline.Record, error) {
			return baseline.Record{}, failure
		})
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe backend result", err)
		}
	}
	retired, err := record.Transition(record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "reviewer", Reason: "retired"}, time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []baseline.Record{retired, {}} {
		if _, err := registry.ResolveIntegration(context.Background(), "alice", claims, func(context.Context, string, string, string) (baseline.Record, error) { return changed, nil }); !errors.Is(err, run.ErrForbidden) {
			t.Fatal("invalid baseline accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveIntegration(ctx, "alice", claims, read); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
}

func TestIntegrationBundleValidation(t *testing.T) {
	entry, _, _, _ := integrationEntry(t)
	for _, mutate := range []func(*ReportPolicyEntry){
		func(e *ReportPolicyEntry) { e.Integration.Service.SHA256 = "invalid" },
		func(e *ReportPolicyEntry) { e.Integration.RequestIssuer = "https://user:secret@example.com" },
		func(e *ReportPolicyEntry) { e.Integration.Candidate.GitSHA = "invalid" },
		func(e *ReportPolicyEntry) {
			e.Integration.Candidate.Image = "ghcr.io/other@sha256:" + strings.Repeat("a", 64)
		},
		func(e *ReportPolicyEntry) { e.Integration.Evidence = nil },
		func(e *ReportPolicyEntry) {
			e.Integration.Evidence = append(e.Integration.Evidence, e.Integration.Evidence[0])
		},
		func(e *ReportPolicyEntry) { e.CatalogOwnerRoute = nil },
	} {
		candidate, _, _, _ := integrationEntry(t)
		mutate(&candidate)
		if _, err := NewReportPolicyRegistry([]ReportPolicyEntry{candidate}); !errors.Is(err, run.ErrValidation) {
			t.Fatal("invalid binding loaded", err)
		}
	}
	source, _, _ := registryBundleFixture(t)
	var bundle reportPolicyBundle
	if err := json.Unmarshal(source[registryManifestPath].Data, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Entries[0].Integration = entry.Integration
	bundle.Entries[0].CatalogOwnerRoute = entry.CatalogOwnerRoute
	for _, enabled := range []bool{false, true} {
		if enabled {
			bundle.PerformanceCatalog = &CatalogConfig{Issuer: "https://provider.example", PrincipalIssuer: "https://auth.example", ProducerVersion: "1.0.0"}
		}
		encoded, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		source[registryManifestPath] = &fstest.MapFile{Data: encoded}
		_, err = LoadReportPolicyRegistry(source, registryManifestPath)
		if enabled && err != nil || !enabled && !errors.Is(err, run.ErrValidation) {
			t.Fatal("integration bundle opt-in", err)
		}
	}
}

func TestIntegrationSLOOnlyAndCancellationDuringBaselineRead(t *testing.T) {
	entry, _, claims, record := integrationEntry(t)
	registry := integrationRegistry(t, entry)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got, err := registry.ResolveIntegration(ctx, "alice", claims, func(context.Context, string, string, string) (baseline.Record, error) {
		cancel()
		return record, nil
	}); !errors.Is(err, context.Canceled) || got != (run.Request{}) {
		t.Fatal("lost cancellation during baseline lookup", err)
	}
	var document map[string]any
	if err := json.Unmarshal(entry.PolicyBytes, &document); err != nil {
		t.Fatal(err)
	}
	for _, rule := range document["spec"].(map[string]any)["rules"].([]any) {
		delete(rule.(map[string]any), "regression")
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	entry.PolicyBytes = encoded
	claims.Policy.SHA256 = sha256Bytes(encoded)
	claims.Baseline = nil
	registry = integrationRegistry(t, entry)
	if _, err := registry.ResolveIntegration(context.Background(), "alice", claims, nil); err != nil {
		t.Fatal("explicit SLO-only policy could not resolve", err)
	}
	pin := catalogReference("https://provider.example", "unsolicited", "1", strings.Repeat("a", 64))
	claims.Baseline = &pin
	if _, err := registry.ResolveIntegration(context.Background(), "alice", claims, nil); !errors.Is(err, run.ErrForbidden) {
		t.Fatal("SLO-only policy accepted an unsolicited baseline", err)
	}
}
