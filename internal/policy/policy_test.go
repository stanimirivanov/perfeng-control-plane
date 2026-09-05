package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func policyFixture(t *testing.T) []byte {
	t.Helper()
	data, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func policyObject(t *testing.T) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(policyFixture(t), &document); err != nil {
		t.Fatal(err)
	}

	return document
}

func encodePolicy(t *testing.T, document any) []byte {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func policyRule(document map[string]any) map[string]any {
	return document["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
}

func TestParsePerformancePolicy(t *testing.T) {
	document, err := Parse(policyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if document.Metadata.Name != "search-browser" || document.Spec.Mode != "inform" ||
		len(document.Spec.Rules) != 1 || document.Spec.Rules[0].Regression == nil {
		t.Fatalf("policy = %#v", document)
	}
}

func TestParseRejectsMalformedAndNonExactDocuments(t *testing.T) {
	valid := string(policyFixture(t))
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "empty"},
		{name: "malformed", data: []byte("{")},
		{name: "duplicate", data: []byte(strings.Replace(
			valid, `"kind":`, `"kind":"Other","kind":`, 1,
		))},
		{name: "unknown", data: []byte(strings.Replace(valid, "{", `{"unknown":true,`, 1))},
		{name: "null quality", data: []byte(strings.Replace(
			valid, `"quality": {`, `"quality": {"maxCv":null,`, 1,
		))},
		{name: "empty quality", data: []byte(strings.Replace(
			valid, `"quality": {`+"\n          "+`"minSamples": 2`+"\n        }", `"quality": {}`, 1,
		))},
		{name: "oversize", data: append(policyFixture(t), make([]byte, maximumPolicyBytes)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.data); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPolicyValidationRules(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "mode", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["mode"] = "block"
		}},
		{name: "missing data", mutate: func(document map[string]any) {
			document["spec"].(map[string]any)["missingData"] = "pass"
		}},
		{name: "owner", mutate: func(document map[string]any) {
			document["metadata"].(map[string]any)["owner"] = " "
		}},
		{name: "metric", mutate: func(document map[string]any) {
			policyRule(document)["metric"].(map[string]any)["name"] = "Invalid"
		}},
		{name: "statistic", mutate: func(document map[string]any) {
			policyRule(document)["metric"].(map[string]any)["statistic"] = "average"
		}},
		{name: "quality", mutate: func(document map[string]any) {
			policyRule(document)["quality"].(map[string]any)["minSamples"] = 0
		}},
		{name: "slo order", mutate: func(document map[string]any) {
			policyRule(document)["slo"] = map[string]any{"min": 300, "max": 200}
		}},
		{name: "difference", mutate: func(document map[string]any) {
			policyRule(document)["regression"].(map[string]any)["practicalDifference"].(map[string]any)["value"] = 0
		}},
		{name: "baseline", mutate: func(document map[string]any) {
			policyRule(document)["regression"].(map[string]any)["reference"].(map[string]any)["baselineId"] = "Invalid"
		}},
		{name: "duplicate rule", mutate: func(document map[string]any) {
			rules := document["spec"].(map[string]any)["rules"].([]any)
			document["spec"].(map[string]any)["rules"] = append(rules, rules[0])
		}},
		{name: "duplicate selector", mutate: func(document map[string]any) {
			rules := document["spec"].(map[string]any)["rules"].([]any)
			copy := map[string]any{}
			for key, value := range rules[0].(map[string]any) {
				copy[key] = value
			}
			copy["id"] = "another-rule"
			document["spec"].(map[string]any)["rules"] = append(rules, copy)
		}},
		{name: "no outcome", mutate: func(document map[string]any) {
			delete(policyRule(document), "slo")
			delete(policyRule(document), "regression")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := policyObject(t)
			test.mutate(document)
			if _, err := Parse(encodePolicy(t, document)); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func reportFixture(t *testing.T, policyBytes []byte) analysisresult.Manifest {
	t.Helper()
	digest := sha256.Sum256(policyBytes)
	candidateRun := "perf-20260902-130000-a1b2c3d5"
	referenceRun := "perf-20260901-130000-a1b2c3d5"
	referenceID := "66666666-6666-4666-8666-666666666666"
	samples, cv := int64(2), 0.05
	candidate, reference, effect := 140.0, 100.0, 0.4
	manifest := analysisresult.Manifest{
		SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: "0.8.0",
		RunID: candidateRun, TestID: "search-browser", CreatedAt: "2026-09-02T13:01:02Z",
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("a", 64),
		},
		Policy: analysisresult.Policy{
			ID: "search-browser", Version: "1.0.0",
			SHA256: hex.EncodeToString(digest[:]), Mode: "inform",
		},
		CandidateArtifact: run.Artifact{
			ID: "55555555-5555-4555-8555-555555555555", RunID: candidateRun,
			Kind: "normalized", URI: "s3://perfeng/runs/" + candidateRun + "/normalized.json",
			SHA256: strings.Repeat("b", 64), SizeBytes: 100,
			MediaType: "application/json", Format: "normalized-result/v1",
		},
		ReferenceArtifacts: []run.Artifact{{
			ID: referenceID, RunID: referenceRun,
			Kind: "normalized", URI: "s3://perfeng/runs/" + referenceRun + "/normalized.json",
			SHA256: strings.Repeat("c", 64), SizeBytes: 100,
			MediaType: "application/json", Format: "normalized-result/v1",
		}},
		Evaluations: []analysisresult.Evaluation{{
			RuleID: "search-latency",
			Metric: analysisresult.Metric{
				Name: "ui.search.action_to_visible_ms", Statistic: "mean", Unit: "ms",
			},
			Quality: analysisresult.Quality{
				Status: "PASS", Reasons: []string{}, Samples: &samples, CV: &cv,
			},
			SLO: analysisresult.SLO{Status: "PASS", Reasons: []string{}, Value: &candidate},
			Regression: analysisresult.Regression{
				Status: "FAIL", Reasons: []string{"Practical threshold reached."},
				CandidateValue: &candidate, ReferenceValue: &reference,
				ReferenceArtifactID: &referenceID,
				Effect:              &analysisresult.Effect{Kind: "relative", Value: effect},
				Method:              &analysisresult.Method{Name: "point-estimate-comparison", Version: "1.0.0"},
			},
		}},
	}
	if err := manifest.Validate(manifest.RunID, manifest.ContractsVersion); err != nil {
		t.Fatal(err)
	}

	return manifest
}

func reportBaselineResolutions(manifest analysisresult.Manifest) []BaselineResolution {
	resolution := BaselineResolution{ID: "approved-search-browser", Version: "1.0.0"}
	if len(manifest.ReferenceArtifacts) > 0 {
		artifact := manifest.ReferenceArtifacts[0]
		resolution.Artifact = &artifact
	}

	return []BaselineResolution{resolution}
}

func TestVerdictApproverAcceptsConsistentReport(t *testing.T) {
	policyBytes := policyFixture(t)
	manifest := reportFixture(t, policyBytes)
	if err := (VerdictApprover{}).ApproveReportVerdicts(
		context.Background(), policyBytes, reportBaselineResolutions(manifest), manifest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictApproverArithmeticVariants(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutatePolicy func(map[string]any)
		mutateReport func(*analysisresult.Manifest)
	}{
		{
			name: "inclusive SLO maximum",
			mutateReport: func(manifest *analysisresult.Manifest) {
				value := 200.0
				manifest.Evaluations[0].SLO.Value = &value
				manifest.Evaluations[0].Regression.CandidateValue = &value
				manifest.Evaluations[0].Regression.Effect.Value = 1
			},
		},
		{
			name: "absolute higher-is-better threshold",
			mutatePolicy: func(document map[string]any) {
				regression := policyRule(document)["regression"].(map[string]any)
				regression["direction"] = "higher-is-better"
				regression["practicalDifference"] = map[string]any{
					"kind": "absolute", "value": 20,
				}
			},
			mutateReport: func(manifest *analysisresult.Manifest) {
				candidate := 80.0
				manifest.Evaluations[0].SLO.Value = &candidate
				manifest.Evaluations[0].Regression.CandidateValue = &candidate
				manifest.Evaluations[0].Regression.Effect = &analysisresult.Effect{
					Kind: "absolute", Value: 20,
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := policyObject(t)
			if test.mutatePolicy != nil {
				test.mutatePolicy(document)
			}
			policyBytes := encodePolicy(t, document)
			manifest := reportFixture(t, policyBytes)
			test.mutateReport(&manifest)
			if err := (VerdictApprover{}).ApproveReportVerdicts(
				context.Background(), policyBytes, reportBaselineResolutions(manifest), manifest,
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerdictApproverRejectsContradictoryClaims(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*analysisresult.Manifest)
	}{
		{name: "test", mutate: func(manifest *analysisresult.Manifest) {
			manifest.TestID = "other-test"
		}},
		{name: "policy", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Policy.Mode = "observe"
		}},
		{name: "policy hash", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Policy.SHA256 = strings.Repeat("d", 64)
		}},
		{name: "coverage", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].RuleID = "other-rule"
		}},
		{name: "selector", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].Metric.Unit = "s"
		}},
		{name: "quality", mutate: func(manifest *analysisresult.Manifest) {
			*manifest.Evaluations[0].Quality.Samples = 1
		}},
		{name: "slo", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].SLO.Status = "FAIL"
		}},
		{name: "effect", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].Regression.Effect.Value = 0.3
		}},
		{name: "regression", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].Regression.Status = "PASS"
		}},
		{name: "not evaluated", mutate: func(manifest *analysisresult.Manifest) {
			manifest.Evaluations[0].Regression = analysisresult.Regression{
				Status: "NOT_EVALUATED", Reasons: []string{"Not evaluated."},
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			policyBytes := policyFixture(t)
			manifest := reportFixture(t, policyBytes)
			test.mutate(&manifest)
			if err := (VerdictApprover{}).ApproveReportVerdicts(
				context.Background(), policyBytes, reportBaselineResolutions(manifest), manifest,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestVerdictApproverAcceptsMissingReferenceAsInconclusive(t *testing.T) {
	policyBytes := policyFixture(t)
	manifest := reportFixture(t, policyBytes)
	manifest.ReferenceArtifacts = []run.Artifact{}
	candidate := *manifest.Evaluations[0].Regression.CandidateValue
	manifest.Evaluations[0].Regression = analysisresult.Regression{
		Status: "INCONCLUSIVE", Reasons: []string{"No approved baseline is available."},
		CandidateValue: &candidate,
	}
	if err := (VerdictApprover{}).ApproveReportVerdicts(
		context.Background(), policyBytes, reportBaselineResolutions(manifest), manifest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictApproverHonorsUnconfiguredSections(t *testing.T) {
	document := policyObject(t)
	delete(policyRule(document), "slo")
	policyBytes := encodePolicy(t, document)
	manifest := reportFixture(t, policyBytes)
	manifest.Evaluations[0].SLO = analysisresult.SLO{
		Status: "NOT_EVALUATED", Reasons: []string{"No SLO is configured."},
	}
	if err := (VerdictApprover{}).ApproveReportVerdicts(
		context.Background(), policyBytes, reportBaselineResolutions(manifest), manifest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestVerdictApproverHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (VerdictApprover{}).ApproveReportVerdicts(
		ctx, policyFixture(t), nil, analysisresult.Manifest{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestVerdictApproverBindsRulesToResolvedBaselines(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]BaselineResolution)
	}{
		{name: "missing resolution", mutate: func(resolutions []BaselineResolution) {
			resolutions[0] = BaselineResolution{}
		}},
		{name: "wrong baseline", mutate: func(resolutions []BaselineResolution) {
			resolutions[0].ID = "other-baseline"
		}},
		{name: "wrong version", mutate: func(resolutions []BaselineResolution) {
			resolutions[0].Version = "2.0.0"
		}},
		{name: "missing approved artifact", mutate: func(resolutions []BaselineResolution) {
			resolutions[0].Artifact = nil
		}},
		{name: "wrong approved artifact", mutate: func(resolutions []BaselineResolution) {
			artifact := *resolutions[0].Artifact
			artifact.ID = "77777777-7777-4777-8777-777777777777"
			resolutions[0].Artifact = &artifact
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			policyBytes := policyFixture(t)
			manifest := reportFixture(t, policyBytes)
			resolutions := reportBaselineResolutions(manifest)
			test.mutate(resolutions)
			if err := (VerdictApprover{}).ApproveReportVerdicts(
				context.Background(), policyBytes, resolutions, manifest,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
