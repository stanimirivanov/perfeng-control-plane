package nativeworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func baselineTemplateFixture() BaselineTemplate {
	return BaselineTemplate{
		BaseURL: "https://sut.perfeng.invalid/search",
		Scenario: BaselineScenario{
			CacheProfile: "cold", PageReuse: "per-iteration",
			WarmupIterations: 1, MeasurementIterations: 5,
		},
		HostProfile: QualifiedHostProfile{
			ProfileID: "windows-mainstream", ProfileVersion: "1.0.0",
			Fingerprint: strings.Repeat("9", 64), EvidenceSHA256: strings.Repeat("a", 64),
		},
		BrowserSettings: BaselineBrowserSettings{
			Headless: false, Viewport: BaselineViewport{Width: 1366, Height: 768},
			DeviceScaleFactor: 1,
		},
		MeasurementsMaximumBytes: 1 << 20, ManifestMaximumBytes: 1 << 20,
	}
}

func baselineArtifactFixture() rawresult.ExecutionArtifact {
	return rawresult.ExecutionArtifact{
		Kind: "source-checkout", Repository: "https://example.invalid/perfeng-playwright.git",
		GitSHA: strings.Repeat("b", 40),
		DependencyLock: &rawresult.DependencyLock{
			Path: "pnpm-lock.yaml", SHA256: strings.Repeat("c", 64),
		},
	}
}

func TestBaselineTemplateResolvesContractValidPlan(t *testing.T) {
	template := baselineTemplateFixture()
	workload := rawresult.Identity{
		ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64),
	}
	plan, err := template.Resolve(
		"search-journey", workload, "smoke", "local-demo",
		baselineArtifactFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Assignment.Target.EnvironmentID != "local-demo" ||
		plan.Assignment.Recipe.Environment.ProfileID != "windows-mainstream" ||
		plan.Assignment.Recipe.Environment.EvidenceSHA256 != strings.Repeat("a", 64) ||
		plan.Assignment.Recipe.WorkloadVersion != workload.Version ||
		plan.Assignment.Recipe.WorkloadSHA256 != workload.SHA256 ||
		plan.Assignment.Browser != "chromium" ||
		plan.Assignment.DiagnosticMode != "baseline" ||
		plan.ArtifactPlan[0].Format != "playwright-measurements/v2" ||
		plan.ArtifactPlan[1].Format != "raw-result/v2" {
		t.Fatalf("unexpected baseline plan: %#v", plan)
	}

	leaseBytes, err := contract.Files.ReadFile(
		"snapshot/api/native-worker/v2/examples/lease.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var lease map[string]any
	decoder := json.NewDecoder(bytes.NewReader(leaseBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&lease); err != nil {
		t.Fatal(err)
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var wirePlan map[string]any
	decoder = json.NewDecoder(bytes.NewReader(planBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&wirePlan); err != nil {
		t.Fatal(err)
	}
	lease["assignment"] = wirePlan["assignment"]
	lease["artifactPlan"] = wirePlan["artifactPlan"]
	if err := contract.ValidateNativeWorkerV2Lease(lease); err != nil {
		t.Fatalf("resolved plan violates the pinned v2 lease contract: %v", err)
	}
}

func TestBaselineTemplateRejectsUnreviewedInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BaselineTemplate)
	}{
		{"cache reuse", func(value *BaselineTemplate) { value.Scenario.PageReuse = "per-run" }},
		{"iterations", func(value *BaselineTemplate) { value.Scenario.MeasurementIterations = 0 }},
		{"profile", func(value *BaselineTemplate) { value.HostProfile.ProfileID = "" }},
		{"evidence", func(value *BaselineTemplate) { value.HostProfile.EvidenceSHA256 = "missing" }},
		{"viewport", func(value *BaselineTemplate) { value.BrowserSettings.Viewport.Width = 319 }},
		{"scale", func(value *BaselineTemplate) { value.BrowserSettings.DeviceScaleFactor = 0 }},
		{"artifact size", func(value *BaselineTemplate) { value.ManifestMaximumBytes = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			template := baselineTemplateFixture()
			test.mutate(&template)
			if !errors.Is(template.Validate(), run.ErrValidation) {
				t.Fatal("invalid baseline template was accepted")
			}
		})
	}
	workload := rawresult.Identity{
		ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64),
	}
	artifact := baselineArtifactFixture()
	artifact.Kind = "release-archive"
	if _, err := baselineTemplateFixture().Resolve(
		"search-journey", workload, "smoke", "local-demo",
		artifact,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("unsupported execution artifact error = %v", err)
	}
	invalidTarget := baselineTemplateFixture()
	invalidTarget.BaseURL = "http://sut.invalid"
	if _, err := invalidTarget.Resolve(
		"search-journey", workload, "smoke", "local-demo", baselineArtifactFixture(),
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("unreviewed target error = %v", err)
	}
}

func TestBaselinePlanRejectsChangedOutput(t *testing.T) {
	workload := rawresult.Identity{
		ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64),
	}
	for _, test := range []struct {
		name   string
		mutate func(*BaselinePlan)
	}{
		{"journey", func(plan *BaselinePlan) { plan.Assignment.Recipe.JourneyID = "other" }},
		{"browser", func(plan *BaselinePlan) { plan.Assignment.Browser = "edge" }},
		{"slot order", func(plan *BaselinePlan) {
			plan.ArtifactPlan[0], plan.ArtifactPlan[1] = plan.ArtifactPlan[1], plan.ArtifactPlan[0]
		}},
		{"format", func(plan *BaselinePlan) { plan.ArtifactPlan[0].Format = "unknown/v1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := baselineTemplateFixture().Resolve(
				"search-journey", workload, "smoke", "local-demo",
				baselineArtifactFixture(),
			)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&plan)
			if !errors.Is(plan.Validate(), run.ErrValidation) {
				t.Fatal("invalid baseline plan was accepted")
			}
		})
	}
}

func TestBaselinePlanMatchesOnlyReviewedArtifactSlots(t *testing.T) {
	plan := baselinePlanFixture(t)
	for _, expected := range plan.ArtifactPlan {
		slot, matched := plan.MatchArtifactSlot(expected.Slot, expected.MediaType, expected.MaximumBytes)
		if !matched || slot != expected {
			t.Fatalf("reviewed slot %q = %#v, matched=%t", expected.Slot, slot, matched)
		}
		if _, matched := plan.MatchArtifactSlot(expected.Slot, expected.MediaType, expected.MaximumBytes+1); matched {
			t.Fatalf("oversized %q artifact was accepted", expected.Slot)
		}
	}
	if _, matched := plan.MatchArtifactSlot("trace", "application/json", 1); matched {
		t.Fatal("unreviewed artifact slot was accepted")
	}
	if _, matched := plan.MatchArtifactSlot("measurements", "application/octet-stream", 1); matched {
		t.Fatal("unreviewed artifact media type was accepted")
	}
	plan.ArtifactPlan[0].Format = "unknown/v1"
	if _, matched := plan.MatchArtifactSlot("measurements", "application/json", 1); matched {
		t.Fatal("invalid baseline plan was accepted")
	}
}
