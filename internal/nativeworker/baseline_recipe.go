package nativeworker

import (
	"regexp"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BaselineContractsVersion is the reviewed contract bundle required for the
// candidate Native Worker v2 baseline recipe.
const BaselineContractsVersion = "0.19.0"

var baselineSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var baselineVersionPattern = regexp.MustCompile(
	`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`,
)

// BaselineScenario fixes the cache and iteration meaning of a browser Run.
type BaselineScenario struct {
	CacheProfile          string `json:"cacheProfile"`
	PageReuse             string `json:"pageReuse"`
	WarmupIterations      int    `json:"warmupIterations"`
	MeasurementIterations int    `json:"measurementIterations"`
}

// QualifiedHostProfile is reviewed Linux-host evidence, distinct from the
// Run's SUT environment reference and target URL.
type QualifiedHostProfile struct {
	ProfileID      string `json:"profileId"`
	ProfileVersion string `json:"profileVersion"`
	Fingerprint    string `json:"fingerprint"`
	EvidenceSHA256 string `json:"evidenceSha256"`
}

// BaselineViewport fixes the browser's measured rendering dimensions.
type BaselineViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// BaselineBrowserSettings fixes the browser presentation under measurement.
type BaselineBrowserSettings struct {
	Headless          bool             `json:"headless"`
	Viewport          BaselineViewport `json:"viewport"`
	DeviceScaleFactor float64          `json:"deviceScaleFactor"`
}

// BaselineTemplate contains only reviewed v2 inputs that cannot be derived
// from the immutable Run, workload or raw-producer identity.
type BaselineTemplate struct {
	// HostOperatingSystem requires explicit review before admitting a new host platform.
	HostOperatingSystem      string                  `json:"hostOperatingSystem"`
	BaseURL                  string                  `json:"baseUrl"`
	Scenario                 BaselineScenario        `json:"scenario"`
	HostProfile              QualifiedHostProfile    `json:"hostProfile"`
	BrowserSettings          BaselineBrowserSettings `json:"browserSettings"`
	MeasurementsMaximumBytes int64                   `json:"measurementsMaximumBytes"`
	ManifestMaximumBytes     int64                   `json:"manifestMaximumBytes"`
}

// BaselineRecipe is the complete v2 search-journey measurement recipe.
type BaselineRecipe struct {
	JourneyID       string                  `json:"journeyId"`
	WorkloadVersion string                  `json:"workloadVersion"`
	WorkloadSHA256  string                  `json:"workloadSha256"`
	Scenario        BaselineScenario        `json:"scenario"`
	Environment     QualifiedHostProfile    `json:"environment"`
	BrowserSettings BaselineBrowserSettings `json:"browserSettings"`
}

// BaselineAssignment is the v2 assignment resolved only from reviewed policy
// and immutable Run context, never from worker-selected defaults.
type BaselineAssignment struct {
	CatalogueTestID   string            `json:"catalogueTestId"`
	WorkloadID        string            `json:"workloadId"`
	Profile           string            `json:"profile"`
	Target            Target            `json:"target"`
	Browser           string            `json:"browser"`
	DiagnosticMode    string            `json:"diagnosticMode"`
	ExecutionArtifact ExecutionArtifact `json:"executionArtifact"`
	Recipe            BaselineRecipe    `json:"recipe"`
}

// BaselinePlan is the immutable, copyable v2 assignment and ordered artifact
// plan. It carries no lease capability or issuance authority.
type BaselinePlan struct {
	Assignment   BaselineAssignment `json:"assignment"`
	ArtifactPlan [2]ArtifactSlot    `json:"artifactPlan"`
}

// Validate checks every reviewed input before the template enters a registry.
func (template BaselineTemplate) Validate() error {
	if template.HostOperatingSystem != "linux" || !validBaseURL(template.BaseURL) || !template.Scenario.valid() ||
		!template.HostProfile.valid() || !template.BrowserSettings.valid() ||
		!validBaselineArtifactSize(template.MeasurementsMaximumBytes) ||
		!validBaselineArtifactSize(template.ManifestMaximumBytes) {
		return run.ErrValidation
	}

	return nil
}

// Resolve combines the reviewed template with immutable registry and Run
// identities. The result owns all of its values and does not inspect a worker.
func (template BaselineTemplate) Resolve(
	testID string,
	workload rawresult.Identity,
	profile string,
	targetEnvironmentID string,
	artifact rawresult.ExecutionArtifact,
) (BaselinePlan, error) {
	if template.Validate() != nil || workload.Validate() != nil ||
		!rawresult.ValidResourceID(testID) || !validBaselineProfile(profile) ||
		!rawresult.ValidResourceID(targetEnvironmentID) ||
		artifact.Kind != "source-checkout" {
		return BaselinePlan{}, run.ErrValidation
	}
	plan := BaselinePlan{
		Assignment: BaselineAssignment{
			CatalogueTestID: testID, WorkloadID: workload.ID, Profile: profile,
			Target:  Target{EnvironmentID: targetEnvironmentID, BaseURL: template.BaseURL},
			Browser: "chromium", DiagnosticMode: "baseline",
			ExecutionArtifact: executionArtifact(artifact),
			Recipe: BaselineRecipe{
				JourneyID: "search", WorkloadVersion: workload.Version,
				WorkloadSHA256: workload.SHA256, Scenario: template.Scenario,
				Environment: template.HostProfile, BrowserSettings: template.BrowserSettings,
			},
		},
		ArtifactPlan: [2]ArtifactSlot{
			{Slot: "measurements", Kind: "measurements", MediaType: "application/json",
				Format:       playwrightMeasurementsV2Format,
				MaximumBytes: template.MeasurementsMaximumBytes, Required: true},
			{Slot: "manifest", Kind: "manifest", MediaType: "application/json",
				Format:       rawResultV2Format,
				MaximumBytes: template.ManifestMaximumBytes, Required: true},
		},
	}
	if plan.Validate() != nil {
		return BaselinePlan{}, run.ErrValidation
	}

	return plan, nil
}

// Validate checks the complete v2 plan independently of its template source.
func (plan BaselinePlan) Validate() error {
	a := plan.Assignment
	if !rawresult.ValidResourceID(a.CatalogueTestID) || !validBaselineProfile(a.Profile) ||
		!rawresult.ValidResourceID(a.Target.EnvironmentID) || !validBaseURL(a.Target.BaseURL) ||
		a.Browser != "chromium" || a.DiagnosticMode != "baseline" ||
		a.ExecutionArtifact.Kind != "source-checkout" ||
		!validExecutionArtifact(a.ExecutionArtifact) || a.Recipe.JourneyID != "search" ||
		(rawresult.Identity{ID: a.WorkloadID, Version: a.Recipe.WorkloadVersion,
			SHA256: a.Recipe.WorkloadSHA256}).Validate() != nil ||
		!a.Recipe.Scenario.valid() || !a.Recipe.Environment.valid() ||
		!a.Recipe.BrowserSettings.valid() || !validBaselineSlots(plan.ArtifactPlan) {
		return run.ErrValidation
	}

	return nil
}

// MatchArtifactSlot returns a reviewed v2 output slot only when the declared
// media type and size fit the complete baseline plan.
func (plan BaselinePlan) MatchArtifactSlot(
	name string,
	mediaType string,
	size int64,
) (ArtifactSlot, bool) {
	if plan.Validate() != nil || size < 1 {
		return ArtifactSlot{}, false
	}
	for _, slot := range plan.ArtifactPlan {
		if slot.Slot == name && slot.MediaType == mediaType && size <= slot.MaximumBytes {
			return slot, true
		}
	}

	return ArtifactSlot{}, false
}

func (scenario BaselineScenario) valid() bool {
	return (scenario.CacheProfile == "cold" || scenario.CacheProfile == "warm") &&
		(scenario.PageReuse == "per-iteration" || scenario.PageReuse == "per-run") &&
		(scenario.CacheProfile != "cold" || scenario.PageReuse == "per-iteration") &&
		scenario.WarmupIterations >= 0 && scenario.WarmupIterations <= 100 &&
		scenario.MeasurementIterations >= 1 && scenario.MeasurementIterations <= 1000
}

func (profile QualifiedHostProfile) valid() bool {
	return rawresult.ValidResourceID(profile.ProfileID) &&
		baselineVersionPattern.MatchString(profile.ProfileVersion) &&
		baselineSHA256Pattern.MatchString(profile.Fingerprint) &&
		baselineSHA256Pattern.MatchString(profile.EvidenceSHA256)
}

func (settings BaselineBrowserSettings) valid() bool {
	return settings.Viewport.Width >= 320 && settings.Viewport.Width <= 3840 &&
		settings.Viewport.Height >= 240 && settings.Viewport.Height <= 2160 &&
		settings.DeviceScaleFactor >= 0.5 && settings.DeviceScaleFactor <= 4
}

func validBaselineArtifactSize(value int64) bool {
	return value >= 1 && value <= maximumArtifactBytes
}

func validBaselineSlots(plan [2]ArtifactSlot) bool {
	return plan[0].Slot == "measurements" && plan[0].Kind == "measurements" &&
		plan[0].MediaType == "application/json" &&
		plan[0].Format == playwrightMeasurementsV2Format && plan[0].Required &&
		validBaselineArtifactSize(plan[0].MaximumBytes) &&
		plan[1].Slot == "manifest" && plan[1].Kind == "manifest" &&
		plan[1].MediaType == "application/json" &&
		plan[1].Format == rawResultV2Format && plan[1].Required &&
		validBaselineArtifactSize(plan[1].MaximumBytes)
}

func validBaselineProfile(value string) bool {
	switch value {
	case "smoke", "average", "regression", "stress", "capacity", "soak":
		return true
	default:
		return false
	}
}
