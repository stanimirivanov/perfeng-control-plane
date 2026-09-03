package rawresult

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

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
	if manifest.RunID != testRunID || len(manifest.Artifacts) != 2 {
		t.Fatalf("Parse() = %#v", manifest)
	}

	encoded[0] = '['
	if manifest.Kind != "RawResult" {
		t.Fatal("parsed manifest aliases input bytes")
	}
}

func TestParseRejectsInvalidJSONEnvelope(t *testing.T) {
	t.Parallel()

	valid := string(encodeManifest(t, validManifest()))
	tests := map[string][]byte{
		"empty":              nil,
		"malformed":          []byte(`{"schemaVersion":`),
		"invalid UTF-8":      {0xff},
		"trailing value":     []byte(valid + ` {}`),
		"unknown field":      []byte(strings.Replace(valid, `"kind":"RawResult"`, `"kind":"RawResult","extra":true`, 1)),
		"wrong field case":   []byte(strings.Replace(valid, `"runId"`, `"RunId"`, 1)),
		"duplicate key":      []byte(strings.Replace(valid, `"kind":"RawResult"`, `"kind":"RawResult","kind":"RawResult"`, 1)),
		"nested unknown":     []byte(strings.Replace(valid, `"workload":{"id"`, `"workload":{"extra":true,"id"`, 1)),
		"nested duplicate":   []byte(strings.Replace(valid, `"producer":{"name":"k6"`, `"producer":{"name":"k6","name":"k6"`, 1)),
		"artifact unknown":   []byte(strings.Replace(valid, `"artifacts":[{"id"`, `"artifacts":[{"extra":true,"id"`, 1)),
		"artifact duplicate": []byte(strings.Replace(valid, `"mediaType":"application/json"`, `"mediaType":"application/json","mediaType":"application/json"`, 1)),
		"excessive nesting":  []byte(`{"extra":` + strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70) + `}`),
		"too large":          append([]byte(`{"padding":"`), make([]byte, maximumManifestBytes)...),
	}

	for name, input := range tests {
		input := input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, input, testRunID, testContractsVersion)
		})
	}
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

func TestParseRejectsUnexpectedContext(t *testing.T) {
	t.Parallel()

	valid := encodeManifest(t, validManifest())
	tests := []struct {
		name             string
		runID            string
		contractsVersion string
	}{
		{name: "invalid expected run", runID: "wrong", contractsVersion: testContractsVersion},
		{name: "different run", runID: "perf-20260903-120000-deadbeef", contractsVersion: testContractsVersion},
		{name: "invalid expected contracts version", runID: testRunID, contractsVersion: "latest"},
		{name: "different contracts version", runID: testRunID, contractsVersion: "0.5.0"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, valid, test.runID, test.contractsVersion)
		})
	}
}

func TestManifestValidateRejectsInvalidClaims(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"schema version":      func(m *Manifest) { m.SchemaVersion = 2 },
		"kind":                func(m *Manifest) { m.Kind = "raw-result" },
		"test ID":             func(m *Manifest) { m.TestID = "Checkout_API" },
		"workload ID":         func(m *Manifest) { m.Workload.ID = "checkout_api" },
		"workload version":    func(m *Manifest) { m.Workload.Version = "01.0.0" },
		"workload hash":       func(m *Manifest) { m.Workload.SHA256 = strings.Repeat("A", 64) },
		"producer name":       func(m *Manifest) { m.Producer.Name = "K6" },
		"producer version":    func(m *Manifest) { m.Producer.Version = "2.2" },
		"producer image":      func(m *Manifest) { m.Producer.Image = "ghcr.io/example/perfeng-k6:latest" },
		"start syntax":        func(m *Manifest) { m.MeasurementWindow.Start = "2026-09-03 12:00:00Z" },
		"timestamp precision": func(m *Manifest) { m.MeasurementWindow.End = "2026-09-03T12:01:00.1234567Z" },
		"empty window":        func(m *Manifest) { m.MeasurementWindow.End = m.MeasurementWindow.Start },
		"created before end":  func(m *Manifest) { m.CreatedAt = "2026-09-03T12:00:30Z" },
		"no artifacts":        func(m *Manifest) { m.Artifacts = nil },
		"artifact run":        func(m *Manifest) { m.Artifacts[0].RunID = "perf-20260903-120000-deadbeef" },
		"normalized artifact": func(m *Manifest) { m.Artifacts[0].Kind = "normalized" },
		"invalid artifact":    func(m *Manifest) { m.Artifacts[0].SHA256 = "invalid" },
		"duplicate ID":        func(m *Manifest) { m.Artifacts[1].ID = m.Artifacts[0].ID },
		"duplicate URI":       func(m *Manifest) { m.Artifacts[1].URI = m.Artifacts[0].URI },
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
			assertValidationError(t, encodeManifest(t, manifest), testRunID, testContractsVersion)
		})
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:    1,
		Kind:             "RawResult",
		ContractsVersion: testContractsVersion,
		RunID:            testRunID,
		TestID:           "checkout-api",
		Workload: Identity{
			ID:      "checkout-smoke",
			Version: "1.0.0",
			SHA256:  strings.Repeat("a", 64),
		},
		Producer: Producer{
			Name:    "k6",
			Version: "2.2.0",
			Image:   "ghcr.io/stanimirivanov/perfeng-k6@sha256:" + strings.Repeat("b", 64),
		},
		MeasurementWindow: Window{
			Start: "2026-09-03T12:00:00Z",
			End:   "2026-09-03T12:01:00.123Z",
		},
		CreatedAt: "2026-09-03T12:01:01Z",
		Artifacts: []run.Artifact{
			{
				ID:        "11111111-1111-4111-8111-111111111111",
				RunID:     testRunID,
				Kind:      "raw",
				URI:       "s3://perfeng-artifacts/runs/" + testRunID + "/summary.json",
				SHA256:    strings.Repeat("c", 64),
				SizeBytes: 1024,
				MediaType: "application/json",
				Format:    "k6-summary-json",
			},
			{
				ID:        "22222222-2222-4222-8222-222222222222",
				RunID:     testRunID,
				Kind:      "raw",
				URI:       "s3://perfeng-artifacts/runs/" + testRunID + "/points.jsonl",
				SHA256:    strings.Repeat("d", 64),
				SizeBytes: 4096,
				MediaType: "application/x-ndjson",
				Format:    "k6-json-points",
			},
		},
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
