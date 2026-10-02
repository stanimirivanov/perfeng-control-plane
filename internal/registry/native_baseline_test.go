package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func nativeBaselineTemplate() *nativeworker.BaselineTemplate {
	return &nativeworker.BaselineTemplate{
		Scenario: nativeworker.BaselineScenario{
			CacheProfile: "cold", PageReuse: "per-iteration",
			WarmupIterations: 1, MeasurementIterations: 5,
		},
		HostProfile: nativeworker.QualifiedHostProfile{
			ProfileID: "windows-mainstream", ProfileVersion: "1.0.0",
			Fingerprint: strings.Repeat("9", 64), EvidenceSHA256: strings.Repeat("a", 64),
		},
		BrowserSettings: nativeworker.BaselineBrowserSettings{
			Viewport:          nativeworker.BaselineViewport{Width: 1366, Height: 768},
			DeviceScaleFactor: 1,
		},
		MeasurementsMaximumBytes: 1 << 20, ManifestMaximumBytes: 1 << 20,
	}
}

func nativeBaselineEntry(t *testing.T) (ReportPolicyEntry, run.Run) {
	t.Helper()
	entry, current := v2RawPolicyFixture(t, "source-checkout")
	entry.ContractsVersion = nativeworker.BaselineContractsVersion
	entry.NativeBaseline = nativeBaselineTemplate()
	current.State = run.StateCreated

	return entry, current
}

func TestRegistryResolvesReviewedNativeBaselineWithoutV1Fallback(t *testing.T) {
	entry, current := nativeBaselineEntry(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	wantRunnerRevision := entry.RawProducer.Artifact.GitSHA
	entry.NativeBaseline.HostProfile.EvidenceSHA256 = strings.Repeat("0", 64)
	entry.NativeBaseline.Scenario.MeasurementIterations = 99
	entry.RawProducer.Artifact.GitSHA = strings.Repeat("0", 40)

	plan, err := registry.ResolveNativeBaselinePlan(context.Background(), "alice", current)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Assignment.CatalogueTestID != current.Request.TestSuite ||
		plan.Assignment.WorkloadID != "browser-smoke" ||
		plan.Assignment.Target.EnvironmentID != current.Request.Environment.ID ||
		plan.Assignment.Recipe.Environment.ProfileID != "windows-mainstream" ||
		plan.Assignment.Recipe.Environment.EvidenceSHA256 != strings.Repeat("a", 64) ||
		plan.Assignment.Recipe.Scenario.MeasurementIterations != 5 ||
		plan.Assignment.ExecutionArtifact.Revision != wantRunnerRevision {
		t.Fatalf("unexpected reviewed baseline plan: %#v", plan)
	}
	plan.Assignment.Recipe.Environment.Fingerprint = strings.Repeat("0", 64)
	plan.ArtifactPlan[0].MaximumBytes = 1
	again, err := registry.ResolveNativeBaselinePlan(context.Background(), "alice", current)
	if err != nil || again.Assignment.Recipe.Environment.Fingerprint != strings.Repeat("9", 64) ||
		again.ArtifactPlan[0].MaximumBytes != 1<<20 {
		t.Fatal("resolved baseline plan aliases registry state", err)
	}
	if _, err := registry.ResolveNativePlan(
		context.Background(), "alice", current,
	); !errors.Is(err, run.ErrForbidden) {
		t.Fatalf("v1 resolver accepted a v2 baseline Run: %v", err)
	}
}

func TestRegistryRejectsInvalidNativeBaselineConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ReportPolicyEntry)
	}{
		{"wrong bundle", func(entry *ReportPolicyEntry) { entry.ContractsVersion = "0.17.0" }},
		{"invalid recipe", func(entry *ReportPolicyEntry) {
			entry.NativeBaseline.Scenario.PageReuse = "per-run"
		}},
		{"missing v1 context", func(entry *ReportPolicyEntry) { entry.NativeAssignment = nil }},
		{"release archive", func(entry *ReportPolicyEntry) {
			entry.RawProducer.Artifact.Kind = "release-archive"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, _ := nativeBaselineEntry(t)
			test.mutate(&entry)
			if _, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry}); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("invalid configuration error = %v", err)
			}
		})
	}
}

func TestNativeBaselineResolutionRejectsUnapprovedContext(t *testing.T) {
	entry, current := nativeBaselineEntry(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		mutate    func(*run.Run)
		want      error
	}{
		{"principal", "mallory", nil, run.ErrForbidden},
		{"state", "alice", func(value *run.Run) { value.State = run.StateRunning }, run.ErrValidation},
		{"candidate", "alice", func(value *run.Run) {
			value.Request.Candidate.Image = "ghcr.io/example/unapproved@sha256:" + strings.Repeat("f", 64)
		}, run.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := current
			if test.mutate != nil {
				test.mutate(&changed)
			}
			_, err := registry.ResolveNativeBaselinePlan(
				context.Background(), test.principal, changed,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveNativeBaselinePlan(ctx, "alice", current); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestBundleLoadsReviewedNativeBaseline(t *testing.T) {
	entry, current := nativeBaselineEntry(t)
	bundle := reportPolicyBundle{
		SchemaVersion: 1,
		Entries: []reportPolicyBundleEntry{{
			Policy: registryPolicyPath, TestID: entry.TestID,
			ContractsVersion: entry.ContractsVersion,
			Catalogue:        entry.Catalogue, Profile: entry.Profile,
			RawProducer: entry.RawProducer, NormalizerProducer: entry.NormalizerProducer,
			Normalization: normalizationBundleConfig{
				SourceFormat: reconcile.PlaywrightMeasurementsV2Format,
			},
			ReportProducer: entry.ReportProducer, NativeAssignment: entry.NativeAssignment,
			NativeBaseline: entry.NativeBaseline,
			Workload:       entry.Workload, Environment: entry.Environment, Dataset: entry.Dataset,
			CandidateImages: entry.CandidateImages, Principals: entry.Principals,
		}},
	}
	manifest, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{
		registryManifestPath: &fstest.MapFile{Data: manifest, Mode: 0o444},
		registryPolicyPath:   &fstest.MapFile{Data: entry.PolicyBytes, Mode: 0o444},
	}
	loaded, err := LoadReportPolicyRegistry(source, registryManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ResolveNativeBaselinePlan(
		context.Background(), "alice", current,
	); err != nil {
		t.Fatal(err)
	}
}
