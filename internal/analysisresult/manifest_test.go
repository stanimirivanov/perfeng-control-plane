package analysisresult

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	testRunID            = "perf-20260904-120000-1234abcd"
	testContractsVersion = "0.6.0"
)

func TestParseMissingReferenceReport(t *testing.T) {
	t.Parallel()

	data := encodeManifest(t, validManifest())
	manifest, err := Parse(data, testRunID, testContractsVersion)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RunID != testRunID || len(manifest.ReferenceArtifacts) != 0 ||
		manifest.Evaluations[0].Regression.Status != "INCONCLUSIVE" {
		t.Fatalf("Parse() = %#v", manifest)
	}

	data[0] = '['
	manifest.Evaluations[0].RuleID = "changed"
	if validManifest().Evaluations[0].RuleID == "changed" {
		t.Fatal("fixture aliases parsed report")
	}
}

func TestParseDecisiveReferenceComparison(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	reference := referenceArtifact()
	manifest.ReferenceArtifacts = []run.Artifact{reference}
	manifest.Evaluations[0].Regression = Regression{
		Status: "FAIL", Reasons: []string{"The practical regression threshold was exceeded."},
		CandidateValue: floatPointer(140), ReferenceValue: floatPointer(100),
		ReferenceArtifactID: stringPointer(reference.ID),
		Effect:              &Effect{Kind: "relative", Value: 0.4},
		Method:              &Method{Name: "point-comparison", Version: "1.0.0"},
	}

	parsed, err := Parse(encodeManifest(t, manifest), testRunID, testContractsVersion)
	if err != nil || parsed.Evaluations[0].Regression.Effect.Value != 0.4 {
		t.Fatalf("Parse() = %#v, %v", parsed, err)
	}
}

func TestParseRejectsInvalidJSONShape(t *testing.T) {
	t.Parallel()

	valid := string(encodeManifest(t, validManifest()))
	tests := map[string][]byte{
		"empty":            nil,
		"malformed":        []byte(`{"schemaVersion":`),
		"invalid UTF-8":    {0xff},
		"trailing value":   []byte(valid + ` {}`),
		"unknown root":     replace(valid, `"kind":"AnalysisResult"`, `"kind":"AnalysisResult","extra":true`),
		"duplicate root":   replace(valid, `"kind":"AnalysisResult"`, `"kind":"AnalysisResult","kind":"AnalysisResult"`),
		"producer field":   replace(valid, `"producer":{"name"`, `"producer":{"extra":true,"name"`),
		"policy field":     replace(valid, `"policy":{"id"`, `"policy":{"extra":true,"id"`),
		"candidate field":  replace(valid, `"candidateArtifact":{"id"`, `"candidateArtifact":{"extra":true,"id"`),
		"evaluation field": replace(valid, `"evaluations":[{"ruleId"`, `"evaluations":[{"extra":true,"ruleId"`),
		"metric field":     replace(valid, `"metric":{"name"`, `"metric":{"extra":true,"name"`),
		"quality field":    replace(valid, `"quality":{"status"`, `"quality":{"extra":true,"status"`),
		"null samples":     replace(valid, `"samples":2`, `"samples":null`),
		"null references":  replace(valid, `"referenceArtifacts":[]`, `"referenceArtifacts":null`),
		"null pass reasons": replace(
			valid,
			`"quality":{"status":"PASS","reasons":[]`,
			`"quality":{"status":"PASS","reasons":null`,
		),
		"empty decisive SLO": replace(
			valid,
			`"slo":{"status":"PASS","reasons":[],"value":140}`,
			`"slo":{"status":"PASS","reasons":[]}`,
		),
		"excessive nesting": []byte(`{"evaluations":` + strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70) + `}`),
		"too large":         make([]byte, maximumReportBytes+1),
	}

	for name, data := range tests {
		data := data
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, data)
		})
	}
}

func TestManifestRejectsInvalidEnvelopeClaims(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"schema":            func(m *Manifest) { m.SchemaVersion = 2 },
		"kind":              func(m *Manifest) { m.Kind = "NormalizedResult" },
		"contracts version": func(m *Manifest) { m.ContractsVersion = "latest" },
		"run":               func(m *Manifest) { m.RunID = "invalid" },
		"test":              func(m *Manifest) { m.TestID = "Search Browser" },
		"created":           func(m *Manifest) { m.CreatedAt = "yesterday" },
		"producer":          func(m *Manifest) { m.Producer.Image = "latest" },
		"policy identity":   func(m *Manifest) { m.Policy.SHA256 = "bad" },
		"policy mode":       func(m *Manifest) { m.Policy.Mode = "block" },
		"blocking":          func(m *Manifest) { m.Blocking = true },
		"candidate run":     func(m *Manifest) { m.CandidateArtifact.RunID = referenceRunID },
		"candidate format":  func(m *Manifest) { m.CandidateArtifact.Format = "analysis-result/v1" },
		"no evaluations":    func(m *Manifest) { m.Evaluations = nil },
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

func TestManifestRejectsInvalidReferenceClaims(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"candidate reused": func(m *Manifest) {
			reference := referenceArtifact()
			reference.ID = m.CandidateArtifact.ID
			m.ReferenceArtifacts = []run.Artifact{reference}
		},
		"candidate URI reused": func(m *Manifest) {
			reference := referenceArtifact()
			reference.URI = m.CandidateArtifact.URI
			m.ReferenceArtifacts = []run.Artifact{reference}
		},
		"same run": func(m *Manifest) {
			reference := referenceArtifact()
			reference.RunID = testRunID
			m.ReferenceArtifacts = []run.Artifact{reference}
		},
		"raw reference": func(m *Manifest) {
			reference := referenceArtifact()
			reference.Kind = "raw"
			m.ReferenceArtifacts = []run.Artifact{reference}
		},
		"duplicate reference": func(m *Manifest) {
			reference := referenceArtifact()
			m.ReferenceArtifacts = []run.Artifact{reference, reference}
		},
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

func TestManifestRejectsInvalidEvaluations(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Manifest){
		"rule ID":        func(m *Manifest) { m.Evaluations[0].RuleID = "Search Rule" },
		"metric name":    func(m *Manifest) { m.Evaluations[0].Metric.Name = "Search Duration" },
		"statistic":      func(m *Manifest) { m.Evaluations[0].Metric.Statistic = "average" },
		"unit":           func(m *Manifest) { m.Evaluations[0].Metric.Unit = " " },
		"quality status": func(m *Manifest) { m.Evaluations[0].Quality.Status = "UNKNOWN" },
		"zero samples":   func(m *Manifest) { *m.Evaluations[0].Quality.Samples = 0 },
		"negative CV":    func(m *Manifest) { m.Evaluations[0].Quality.CV = floatPointer(-0.1) },
		"empty reason": func(m *Manifest) {
			m.Evaluations[0].Regression.Reasons = []string{" "}
		},
		"duplicate reason": func(m *Manifest) {
			m.Evaluations[0].Regression.Reasons = []string{"missing", "missing"}
		},
		"failed SLO without value": func(m *Manifest) {
			m.Evaluations[0].SLO = SLO{Status: "FAIL", Reasons: []string{"too slow"}}
		},
		"decisive outcome with bad quality": func(m *Manifest) {
			m.Evaluations[0].Quality = Quality{
				Status: "UNSTABLE", Reasons: []string{"too variable"},
			}
		},
		"unknown reference": func(m *Manifest) {
			m.Evaluations[0].Regression = decisiveRegression("99999999-9999-4999-8999-999999999999")
		},
		"inconclusive unknown reference": func(m *Manifest) {
			m.Evaluations[0].Regression.ReferenceArtifactID = stringPointer(
				"99999999-9999-4999-8999-999999999999",
			)
		},
		"inconclusive invalid effect": func(m *Manifest) {
			m.Evaluations[0].Regression.Effect = &Effect{Kind: "percentage", Value: 40}
		},
		"inconclusive invalid method": func(m *Manifest) {
			m.Evaluations[0].Regression.Method = &Method{Name: " ", Version: "latest"}
		},
		"missing comparison method": func(m *Manifest) {
			reference := referenceArtifact()
			m.ReferenceArtifacts = []run.Artifact{reference}
			regression := decisiveRegression(reference.ID)
			regression.Method = nil
			m.Evaluations[0].Regression = regression
		},
		"duplicate rule": func(m *Manifest) {
			m.Evaluations = append(m.Evaluations, m.Evaluations[0])
		},
		"duplicate metric": func(m *Manifest) {
			second := m.Evaluations[0]
			second.RuleID = "another-rule"
			m.Evaluations = append(m.Evaluations, second)
		},
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

func TestSharedIdentityValidation(t *testing.T) {
	t.Parallel()

	if !rawresult.ValidResourceID("search-browser") || rawresult.ValidResourceID("Search Browser") {
		t.Fatal("resource ID validation disagrees with the contract")
	}
	if !rawresult.ValidTimestamp("2026-09-04T12:01:00Z") || rawresult.ValidTimestamp("yesterday") {
		t.Fatal("timestamp validation disagrees with the contract")
	}
	if (rawresult.Identity{
		ID: "search-browser", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
	}).Validate() != nil {
		t.Fatal("valid shared identity rejected")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(encodeManifest(f, validManifest()))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		manifest, err := Parse(data, testRunID, testContractsVersion)
		if err == nil && manifest.Validate(testRunID, testContractsVersion) != nil {
			t.Fatal("Parse() returned an invalid report")
		}
	})
}

const referenceRunID = "perf-20260903-120000-deadbeef"

func validManifest() Manifest {
	return Manifest{
		SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: testContractsVersion,
		RunID: testRunID, TestID: "search-browser", CreatedAt: "2026-09-04T12:01:00Z",
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("b", 64),
		},
		Policy: Policy{
			ID: "search-browser", Version: "1.0.0", SHA256: strings.Repeat("c", 64), Mode: "inform",
		},
		CandidateArtifact:  candidateArtifact(),
		ReferenceArtifacts: []run.Artifact{},
		Evaluations: []Evaluation{{
			RuleID:  "search-latency",
			Metric:  Metric{Name: "ui.search.action_to_visible_ms", Statistic: "mean", Unit: "ms"},
			Quality: Quality{Status: "PASS", Reasons: []string{}, Samples: int64Pointer(2)},
			SLO:     SLO{Status: "PASS", Reasons: []string{}, Value: floatPointer(140)},
			Regression: Regression{
				Status: "INCONCLUSIVE", Reasons: []string{"The approved reference is unavailable."},
				CandidateValue: floatPointer(140),
			},
		}},
	}
}

func candidateArtifact() run.Artifact {
	return run.Artifact{
		ID: "44444444-4444-4444-8444-444444444444", RunID: testRunID, Kind: "normalized",
		URI:    "s3://perfeng-artifacts/runs/" + testRunID + "/normalized/result.json",
		SHA256: strings.Repeat("d", 64), SizeBytes: 1502,
		MediaType: "application/json", Format: "normalized-result/v1",
	}
}

func referenceArtifact() run.Artifact {
	return run.Artifact{
		ID: "66666666-6666-4666-8666-666666666666", RunID: referenceRunID, Kind: "normalized",
		URI:    "s3://perfeng-artifacts/runs/" + referenceRunID + "/normalized/result.json",
		SHA256: strings.Repeat("e", 64), SizeBytes: 1490,
		MediaType: "application/json", Format: "normalized-result/v1",
	}
}

func decisiveRegression(referenceID string) Regression {
	return Regression{
		Status: "FAIL", Reasons: []string{"regression"},
		CandidateValue: floatPointer(140), ReferenceValue: floatPointer(100),
		ReferenceArtifactID: stringPointer(referenceID),
		Effect:              &Effect{Kind: "relative", Value: 0.4},
		Method:              &Method{Name: "point-comparison", Version: "1.0.0"},
	}
}

func encodeManifest(t testing.TB, manifest Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func assertInvalidManifest(t *testing.T, manifest Manifest) {
	t.Helper()
	if !errors.Is(manifest.Validate(testRunID, testContractsVersion), run.ErrValidation) {
		t.Fatal("Validate() did not return ErrValidation")
	}
	assertValidationError(t, encodeManifest(t, manifest))
}

func assertValidationError(t *testing.T, data []byte) {
	t.Helper()
	manifest, err := Parse(data, testRunID, testContractsVersion)
	if !errors.Is(err, run.ErrValidation) {
		t.Fatalf("Parse() error = %v, want ErrValidation", err)
	}
	if !reflect.DeepEqual(manifest, Manifest{}) {
		t.Fatalf("Parse() manifest = %#v, want zero value", manifest)
	}
}

func replace(input, old, replacement string) []byte {
	return []byte(strings.Replace(input, old, replacement, 1))
}

func stringPointer(value string) *string  { return &value }
func int64Pointer(value int64) *int64     { return &value }
func floatPointer(value float64) *float64 { return &value }
