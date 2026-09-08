package reconcile

import (
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNormalizationSpecAcceptsOnlyExplicitSupportedCombinations(t *testing.T) {
	valid := []NormalizationSpec{
		{SourceFormat: K6SummaryFormat, Layout: K6TypedLayout},
		{SourceFormat: K6SummaryFormat, Layout: K6LegacyExportLayout},
		{SourceFormat: PlaywrightMeasurementsV2Format},
		{SourceFormat: KubeBurnerPodLatencyFormat},
	}
	for _, spec := range valid {
		if err := spec.Validate(); err != nil {
			t.Fatalf("valid spec %+v: %v", spec, err)
		}
	}

	invalid := []NormalizationSpec{
		{},
		{SourceFormat: K6SummaryFormat},
		{SourceFormat: K6SummaryFormat, Layout: "latest"},
		{SourceFormat: PlaywrightMeasurementsV2Format, Layout: K6TypedLayout},
		{SourceFormat: KubeBurnerPodLatencyFormat, Layout: K6TypedLayout},
		{SourceFormat: "browser-observations-json"},
	}
	for _, spec := range invalid {
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid spec accepted: %+v", spec)
		}
	}
}

func TestAnalysisPlanBindsSourceFormatAndApprovedImage(t *testing.T) {
	runID := "perf-20260907-120000-1234abcd"
	plan := AnalysisPlan{
		Source: run.Artifact{
			ID: "11111111-1111-4111-8111-111111111111", RunID: runID, Kind: "raw",
			URI:    "s3://perfeng/runs/" + runID + "/playwright-measurements.json",
			SHA256: strings.Repeat("a", 64), SizeBytes: 100,
			MediaType: "application/json", Format: PlaywrightMeasurementsV2Format,
		},
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "0.8.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("b", 64),
		},
		Spec: NormalizationSpec{SourceFormat: PlaywrightMeasurementsV2Format},
	}
	if err := plan.Validate(runID); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*AnalysisPlan){
		"run":          func(value *AnalysisPlan) { value.Source.RunID = "perf-20260907-120000-deadbeef" },
		"kind":         func(value *AnalysisPlan) { value.Source.Kind = "normalized" },
		"media type":   func(value *AnalysisPlan) { value.Source.MediaType = "application/octet-stream" },
		"format":       func(value *AnalysisPlan) { value.Source.Format = K6SummaryFormat },
		"producer":     func(value *AnalysisPlan) { value.Producer.Image = "latest" },
		"input layout": func(value *AnalysisPlan) { value.Spec.Layout = K6TypedLayout },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := plan
			mutate(&invalid)
			if err := invalid.Validate(runID); err == nil {
				t.Fatal("invalid analysis plan accepted")
			}
		})
	}
}
