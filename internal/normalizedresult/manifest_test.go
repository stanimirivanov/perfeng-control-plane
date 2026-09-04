package normalizedresult

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	testRunID            = "perf-20260903-120000-1234abcd"
	testContractsVersion = "0.4.0"
)

func TestParse(t *testing.T) {
	t.Parallel()

	encoded := encodeManifest(t, validManifest())
	manifest, err := Parse(encoded, testRunID, testContractsVersion)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if manifest.RunID != testRunID || len(manifest.SourceArtifacts) != 1 ||
		len(manifest.Results) != 1 || manifest.Results[0].Distribution.Samples != nil {
		t.Fatalf("Parse() = %#v", manifest)
	}

	encoded[0] = '['
	manifest.Results[0].Metric.Name = "changed"
	if validManifest().Results[0].Metric.Name == "changed" {
		t.Fatal("fixture aliases parsed manifest")
	}
}

func TestParseAcceptsUnavailableSamplesAndOpenMetadata(t *testing.T) {
	t.Parallel()

	encoded := string(encodeManifest(t, validManifest()))
	explicitNull := strings.Replace(encoded, `"distribution":{"mean"`, `"distribution":{"samples":null,"mean"`, 1)
	manifest, err := Parse([]byte(explicitNull), testRunID, testContractsVersion)
	if err != nil || manifest.Results[0].Distribution.Samples != nil {
		t.Fatalf("explicit null samples = %#v, %v", manifest, err)
	}

	withoutThresholds := validManifest()
	withoutThresholds.Results[0].Thresholds = nil
	withoutThresholds.Results[0].Metadata = map[string]any{
		"adapter": "k6", "labels": map[string]any{"route": "/checkout", "cached": false},
	}
	if _, err := Parse(
		encodeManifest(t, withoutThresholds), testRunID, testContractsVersion,
	); err != nil {
		t.Fatalf("open metadata or absent thresholds rejected: %v", err)
	}
}

func TestParseRejectsInvalidJSONShape(t *testing.T) {
	t.Parallel()

	valid := string(encodeManifest(t, validManifest()))
	tests := map[string][]byte{
		"empty":              nil,
		"malformed":          []byte(`{"schemaVersion":`),
		"invalid UTF-8":      {0xff},
		"trailing value":     []byte(valid + ` {}`),
		"unknown root":       replaced(valid, `"kind":"NormalizedResult"`, `"kind":"NormalizedResult","extra":true`),
		"wrong field case":   replaced(valid, `"runId"`, `"RunId"`),
		"duplicate root":     replaced(valid, `"kind":"NormalizedResult"`, `"kind":"NormalizedResult","kind":"NormalizedResult"`),
		"workload unknown":   replaced(valid, `"workload":{"id"`, `"workload":{"extra":true,"id"`),
		"artifact unknown":   replaced(valid, `"sourceArtifacts":[{"id"`, `"sourceArtifacts":[{"extra":true,"id"`),
		"result unknown":     replaced(valid, `"results":[{"schemaVersion"`, `"results":[{"extra":true,"schemaVersion"`),
		"metric unknown":     replaced(valid, `"metric":{"name"`, `"metric":{"extra":true,"name"`),
		"distribution field": replaced(valid, `"distribution":{"mean"`, `"distribution":{"unknown":1,"mean"`),
		"threshold field":    replaced(valid, `"thresholds":{"slo"`, `"thresholds":{"unknown":{},"slo"`),
		"threshold result":   replaced(valid, `"passed":true,"threshold"`, `"passed":true,"unknown":1,"threshold"`),
		"duplicate metadata": replaced(valid, `"normalizer":"fixture"`, `"normalizer":"fixture","normalizer":"fixture"`),
		"excessive nesting":  []byte(`{"metadata":` + strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70) + `}`),
		"too large":          make([]byte, maximumManifestBytes+1),
	}

	for name, input := range tests {
		input := input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, input, testRunID, testContractsVersion)
		})
	}
}

func TestManifestValidateRejectsInvalidEnvelopeClaims(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"schema version":      func(m *Manifest) { m.SchemaVersion = 2 },
		"kind":                func(m *Manifest) { m.Kind = "RawResult" },
		"contracts version":   func(m *Manifest) { m.ContractsVersion = "01.0.0" },
		"run":                 func(m *Manifest) { m.RunID = "invalid" },
		"test":                func(m *Manifest) { m.TestID = "Checkout_API" },
		"workload":            func(m *Manifest) { m.Workload.Version = "v1" },
		"producer":            func(m *Manifest) { m.Producer.Image = "latest" },
		"window":              func(m *Manifest) { m.MeasurementWindow.End = m.MeasurementWindow.Start },
		"created before end":  func(m *Manifest) { m.CreatedAt = "2026-09-03T12:00:30Z" },
		"no sources":          func(m *Manifest) { m.SourceArtifacts = nil },
		"normalized source":   func(m *Manifest) { m.SourceArtifacts[0].Kind = "normalized" },
		"source run":          func(m *Manifest) { m.SourceArtifacts[0].RunID = "perf-20260903-120000-deadbeef" },
		"duplicate source ID": func(m *Manifest) { m.SourceArtifacts = append(m.SourceArtifacts, m.SourceArtifacts[0]) },
		"no results":          func(m *Manifest) { m.Results = nil },
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manifest := validManifest()
			mutate(&manifest)
			assertInvalidManifest(t, manifest)
		})
	}
}

func TestManifestValidateRejectsInvalidResults(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"result schema":       func(m *Manifest) { m.Results[0].SchemaVersion = 1 },
		"result run":          func(m *Manifest) { m.Results[0].RunID = "perf-20260903-120000-deadbeef" },
		"metric name":         func(m *Manifest) { m.Results[0].Metric.Name = "HTTP Duration" },
		"metric direction":    func(m *Manifest) { m.Results[0].Metric.Direction = "neutral" },
		"metric type":         func(m *Manifest) { m.Results[0].Metric.Type = stringPointer("timer") },
		"invalid unit":        func(m *Manifest) { m.Results[0].Metric.Unit = stringPointer("\xff") },
		"zero samples":        func(m *Manifest) { m.Results[0].Distribution.Samples = int64Pointer(0) },
		"negative percentile": func(m *Manifest) { m.Results[0].Distribution.P95 = floatPointer(-1) },
		"negative deviation":  func(m *Manifest) { m.Results[0].Distribution.Stddev = floatPointer(-1) },
		"non-finite mean":     func(m *Manifest) { m.Results[0].Distribution.Mean = floatPointer(math.NaN()) },
		"threshold text": func(m *Manifest) {
			threshold := m.Results[0].Thresholds.SLO["p95"]
			threshold.Threshold = stringPointer("\xff")
			m.Results[0].Thresholds.SLO["p95"] = threshold
		},
		"threshold number": func(m *Manifest) {
			threshold := m.Results[0].Thresholds.SLO["p95"]
			threshold.Actual = floatPointer(math.Inf(1))
			m.Results[0].Thresholds.SLO["p95"] = threshold
		},
		"duplicate metric": func(m *Manifest) { m.Results = append(m.Results, m.Results[0]) },
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manifest := validManifest()
			mutate(&manifest)
			if !errors.Is(manifest.Validate(testRunID, testContractsVersion), run.ErrValidation) {
				t.Fatal("Validate() did not return ErrValidation")
			}
		})
	}
}

func TestParseRejectsMissingThresholdPassed(t *testing.T) {
	t.Parallel()

	encoded := string(encodeManifest(t, validManifest()))
	missing := replaced(encoded, `"passed":true,"threshold"`, `"threshold"`)
	assertValidationError(t, missing, testRunID, testContractsVersion)
}

func FuzzParse(f *testing.F) {
	f.Add(encodeManifest(f, validManifest()))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		manifest, err := Parse(data, testRunID, testContractsVersion)
		if err == nil && manifest.Validate(testRunID, testContractsVersion) != nil {
			t.Fatal("Parse() returned an invalid manifest")
		}
	})
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:    1,
		Kind:             "NormalizedResult",
		ContractsVersion: testContractsVersion,
		RunID:            testRunID,
		TestID:           "checkout-api",
		Workload: rawresult.Identity{
			ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
		},
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "0.1.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("b", 64),
		},
		MeasurementWindow: rawresult.Window{
			Start: "2026-09-03T12:00:00Z", End: "2026-09-03T12:01:00Z",
		},
		CreatedAt: "2026-09-03T12:01:01Z",
		SourceArtifacts: []run.Artifact{{
			ID:        "11111111-1111-4111-8111-111111111111",
			RunID:     testRunID,
			Kind:      "raw",
			URI:       "s3://perfeng-artifacts/runs/" + testRunID + "/summary.json",
			SHA256:    strings.Repeat("c", 64),
			SizeBytes: 1024,
			MediaType: "application/json",
			Format:    "k6-summary-json",
		}},
		Results: []Result{{
			SchemaVersion: 2,
			RunID:         testRunID,
			Metric: Metric{
				Name: "api.http.duration", Direction: "lower-is-better",
				Type: stringPointer("latency"), Unit: stringPointer("ms"),
			},
			Distribution: Distribution{Mean: floatPointer(180), P95: floatPointer(260)},
			Thresholds: &Thresholds{
				SLO: map[string]SLOThreshold{
					"p95": {Passed: true, Threshold: stringPointer("p(95)<300"), Actual: floatPointer(260)},
				},
				Regression: map[string]RegressionThreshold{
					"p95": {
						Passed: true, BaselineValue: floatPointer(250),
						ActualValue: floatPointer(260), PercentChange: floatPointer(4),
					},
				},
			},
			Metadata: map[string]any{"normalizer": "fixture"},
		}},
	}
}

func encodeManifest(t testing.TB, manifest Manifest) []byte {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return encoded
}

func assertInvalidManifest(t *testing.T, manifest Manifest) {
	t.Helper()
	if !errors.Is(manifest.Validate(testRunID, testContractsVersion), run.ErrValidation) {
		t.Fatal("Validate() did not return ErrValidation")
	}
	encoded, err := json.Marshal(manifest)
	if err == nil {
		assertValidationError(t, encoded, testRunID, testContractsVersion)
	}
}

func assertValidationError(t *testing.T, data []byte, runID, contractsVersion string) {
	t.Helper()
	manifest, err := Parse(data, runID, contractsVersion)
	if !errors.Is(err, run.ErrValidation) {
		t.Fatalf("Parse() error = %v, want ErrValidation", err)
	}
	if !reflect.DeepEqual(manifest, Manifest{}) {
		t.Fatalf("Parse() manifest = %#v, want zero value", manifest)
	}
}

func replaced(input, old, replacement string) []byte {
	return []byte(strings.Replace(input, old, replacement, 1))
}

func stringPointer(value string) *string  { return &value }
func int64Pointer(value int64) *int64     { return &value }
func floatPointer(value float64) *float64 { return &value }
