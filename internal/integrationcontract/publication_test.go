package integrationcontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func publicationInput(t *testing.T) PublicationInput {
	t.Helper()
	message, err := DecodeAnalysisPublication(publicationFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	refBytes, err := json.Marshal(message.Data.Native.Baseline.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	var reference run.Artifact
	if err := json.Unmarshal(refBytes, &reference); err != nil {
		t.Fatal(err)
	}
	producer := rawresult.Producer{Name: "perfeng-analysis", Version: "0.1.0", Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("f", 64)}
	candidate := run.Artifact{ID: "44444444-4444-4444-8444-444444444444", RunID: message.Data.Native.RunID, Kind: "normalized", URI: "s3://perfeng-example/candidate.json", SHA256: strings.Repeat("b", 64), SizeBytes: 100, MediaType: "application/json", Format: "normalized-result/v1"}
	samples, value, refValue, effect := int64(2), 140.0, 100.0, 0.4
	manifest := analysisresult.Manifest{SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: "0.4.0", RunID: candidate.RunID, TestID: message.Data.Native.TestID, CreatedAt: "2026-09-02T13:02:00Z", Producer: producer,
		Policy: analysisresult.Policy{ID: message.Data.Native.Policy.ID, Version: message.Data.Native.Policy.Version, SHA256: message.Data.Native.Policy.SHA256, Mode: "inform"}, CandidateArtifact: candidate, ReferenceArtifacts: []run.Artifact{reference},
		Evaluations: []analysisresult.Evaluation{{RuleID: "search-latency", Metric: analysisresult.Metric{Name: "ui.search.action_to_visible_ms", Statistic: "mean", Unit: "ms"},
			Quality: analysisresult.Quality{Status: "PASS", Reasons: []string{}, Samples: &samples}, SLO: analysisresult.SLO{Status: "PASS", Reasons: []string{}, Value: &value},
			Regression: analysisresult.Regression{Status: "FAIL", Reasons: []string{"threshold reached"}, CandidateValue: &value, ReferenceValue: &refValue, ReferenceArtifactID: &reference.ID, Effect: &analysisresult.Effect{Kind: "relative", Value: effect}, Method: &analysisresult.Method{Name: "point-estimate-comparison", Version: "1.0.0"}}}}}
	request := run.Request{TestSuite: manifest.TestID, Catalogue: run.Reference{ID: "native-catalogue", Version: "1.0.0", SHA256: strings.Repeat("a", 64)}, Profile: "smoke",
		Candidate: run.Candidate{GitSHA: strings.Repeat("b", 40), Image: "ghcr.io/example/candidate@" + message.Data.Context.Candidate.ImageDigest}, Environment: run.Reference{ID: "native-env", Version: "1.0.0", SHA256: strings.Repeat("a", 64)}, Policy: run.Reference{ID: manifest.Policy.ID, Version: manifest.Policy.Version, SHA256: manifest.Policy.SHA256}}
	admission := contract.IntegrationMessage{SpecVersion: "1.0", ID: "accepted", Source: message.Source, Type: "dev.perfeng.integration.accepted.v1", Time: "2026-09-02T13:00:01Z", DataContentType: "application/json", DataSchema: integrationSchemaURI, Data: contract.IntegrationData{Context: message.Data.Context, Run: &message.Data.Run}}
	input := PublicationInput{Admission: integration.Record{Disposition: admission, Run: &run.Run{ID: manifest.RunID, Request: request}, Native: &message.Data.Native}, Provider: message.Source, Principal: message.Data.Context.Principal, EventID: "publication-1", PublishedAt: time.Date(2026, 9, 2, 13, 3, 0, 0, time.UTC), PolicyBytes: policyBytes, Producer: producer, ContractsVersion: manifest.ContractsVersion, Candidate: candidate,
		Report: run.Artifact{ID: message.Data.Artifact.ID, RunID: manifest.RunID, Kind: "normalized", URI: message.Data.Artifact.URI, MediaType: "application/json", Format: "analysis-result/v1"}}
	sealPublicationReport(t, &input, manifest)

	return input
}

func sealPublicationReport(t *testing.T, input *PublicationInput, manifest analysisresult.Manifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	input.ReportBytes, input.Report.SHA256, input.Report.SizeBytes = raw, publicationDigest(raw), int64(len(raw))
}

func TestPublicationBuilderPreservesBytesAndOwnsDeterministicOutput(t *testing.T) {
	input := publicationInput(t)
	input.Admission.Run.State = run.StateAborted // Valid late report survives cancellation.
	input.ReportBytes = bytes.Replace(input.ReportBytes, []byte(`"value":140`), []byte(`"value":1.40000000000000000000001e2`), 1)
	input.Report.SHA256, input.Report.SizeBytes = publicationDigest(input.ReportBytes), int64(len(input.ReportBytes))
	one, err := BuildAnalysisPublication(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildAnalysisPublication(context.Background(), input)
	if err != nil || !reflect.DeepEqual(one, two) || one.Data.Artifact.Kind != "analysis" || one.Data.Artifact.SHA256 != input.Report.SHA256 || !input.PublishedAt.After(mustPublicationTime(t, input.Admission.Disposition.Data.Context.Deadline)) {
		t.Fatal("publication lost exact/late evidence", err)
	}
	input.Admission.Native.Baseline.Identity.ID = "mutated"
	input.Admission.Disposition.Data.Context.Evidence[0].ID = "mutated"
	if one.Data.Native.Baseline.Identity.ID == "mutated" || one.Data.Context.Evidence[0].ID == "mutated" {
		t.Fatal("publication aliases retained evidence")
	}
}

func mustPublicationTime(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func TestPublicationBuilderRejectsSubstitutedAuthorities(t *testing.T) {
	for _, mutate := range []func(*PublicationInput){
		func(i *PublicationInput) { i.Admission.Native = nil },
		func(i *PublicationInput) { i.Principal.ID = "other" },
		func(i *PublicationInput) { i.Provider = "https://other.example" },
		func(i *PublicationInput) { i.Report.SHA256 = strings.Repeat("f", 64) },
		func(i *PublicationInput) { i.Report.SizeBytes = 1<<20 + 1 },
		func(i *PublicationInput) { i.Report.Kind = "analysis" },
		func(i *PublicationInput) { i.PolicyBytes = append(i.PolicyBytes, ' ') },
		func(i *PublicationInput) { i.Candidate.ID = "99999999-9999-4999-8999-999999999999" },
		func(i *PublicationInput) { i.ContractsVersion = "0.25.0" },
		func(i *PublicationInput) { i.Producer.Version = "99.0.0" },
		func(i *PublicationInput) { i.Admission.Native.TestID = "other-test" },
		func(i *PublicationInput) { i.Admission.Native.Baseline.Identity.Version = "2.0.0" },
		func(i *PublicationInput) { i.Report.ID = i.Admission.Native.Baseline.Artifact.ID },
		func(i *PublicationInput) { i.PublishedAt = i.PublishedAt.Add(-time.Hour) },
		func(i *PublicationInput) { i.EventID = "" },
	} {
		input := publicationInput(t)
		mutate(&input)
		got, err := BuildAnalysisPublication(context.Background(), input)
		if !errors.Is(err, contract.ErrAnalysisPublication) || !reflect.DeepEqual(got, contract.AnalysisPublication{}) {
			t.Fatal("substitution returned partial publication", err)
		}
	}
}

func TestPublicationBuilderChecksReportPolicyWithoutCollapsingOutcomes(t *testing.T) {
	for _, mode := range []string{"missing-reference", "unknown-quality", "wrong-rule", "foreign-reference", "malformed", "skipped-slo", "early-report", "invalid-unicode", "numeric-limit"} {
		t.Run(mode, func(t *testing.T) {
			input := publicationInput(t)
			manifest, err := analysisresult.Parse(input.ReportBytes, input.Report.RunID, input.ContractsVersion)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "missing-reference":
				manifest.ReferenceArtifacts = []run.Artifact{}
				manifest.Evaluations[0].Regression = analysisresult.Regression{Status: "INCONCLUSIVE", Reasons: []string{"missing reference"}}
			case "unknown-quality":
				manifest.Evaluations[0].Quality = analysisresult.Quality{Status: "INCONCLUSIVE", Reasons: []string{"unknown samples"}}
				manifest.Evaluations[0].SLO = analysisresult.SLO{Status: "INCONCLUSIVE", Reasons: []string{"unknown samples"}}
				manifest.Evaluations[0].Regression = analysisresult.Regression{Status: "INCONCLUSIVE", Reasons: []string{"unknown samples"}}
			case "wrong-rule":
				manifest.Evaluations[0].RuleID = "unconfigured-rule"
			case "foreign-reference":
				manifest.ReferenceArtifacts[0].URI = "https://other.example/baseline.json"
			case "skipped-slo":
				manifest.Evaluations[0].SLO = analysisresult.SLO{Status: "NOT_EVALUATED", Reasons: []string{"skipped"}}
			case "early-report":
				manifest.CreatedAt = "2026-09-01T13:02:00Z"
			}
			sealPublicationReport(t, &input, manifest)
			if mode == "invalid-unicode" {
				input.ReportBytes = bytes.Replace(input.ReportBytes, []byte("threshold reached"), []byte(`\ud800`), 1)
			}
			if mode == "numeric-limit" {
				input.ReportBytes = bytes.Replace(input.ReportBytes, []byte(`"value":140`), []byte(`"value":1e-999999999`), 1)
			}
			if mode == "malformed" {
				input.ReportBytes = []byte(`{"private":"secret"}`)
				input.Report.SHA256 = publicationDigest(input.ReportBytes)
				input.Report.SizeBytes = int64(len(input.ReportBytes))
			}
			input.Report.SHA256, input.Report.SizeBytes = publicationDigest(input.ReportBytes), int64(len(input.ReportBytes))
			got, err := BuildAnalysisPublication(context.Background(), input)
			if mode == "missing-reference" || mode == "unknown-quality" {
				if err != nil || got.Data.Artifact.SHA256 != input.Report.SHA256 {
					t.Fatal("inconclusive evidence rejected", err)
				}
			} else if !errors.Is(err, contract.ErrAnalysisPublication) || !reflect.DeepEqual(got, contract.AnalysisPublication{}) || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid report returned authority", err)
			}
		})
	}
}

func TestPublicationBuilderSupportsReviewedSLOOnlyPolicy(t *testing.T) {
	input := publicationInput(t)
	document, err := policy.Parse(input.PolicyBytes)
	if err != nil {
		t.Fatal(err)
	}
	for index := range document.Spec.Rules {
		document.Spec.Rules[index].Regression = nil
	}
	input.PolicyBytes, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	digest := publicationDigest(input.PolicyBytes)
	input.Admission.Native.Policy.SHA256, input.Admission.Run.Request.Policy.SHA256 = digest, digest
	input.Admission.Native.Baseline, input.Admission.Disposition.Data.Context.Baseline = nil, nil
	manifest, err := analysisresult.Parse(input.ReportBytes, input.Report.RunID, input.ContractsVersion)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Policy.SHA256, manifest.ReferenceArtifacts = digest, []run.Artifact{}
	manifest.Evaluations[0].Regression = analysisresult.Regression{Status: "NOT_EVALUATED", Reasons: []string{"not configured"}}
	sealPublicationReport(t, &input, manifest)
	message, err := BuildAnalysisPublication(context.Background(), input)
	if err != nil || message.Data.Native.Baseline != nil || message.Data.Context.Baseline != nil {
		t.Fatal("SLO-only publication failed", err)
	}
}

func TestPublicationBuilderCancellationAndConcurrentReuse(t *testing.T) {
	input := publicationInput(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := BuildAnalysisPublication(ctx, input); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, contract.AnalysisPublication{}) {
		t.Fatal("cancelled build returned evidence", err)
	}
	for range 4 {
		t.Run("build", func(t *testing.T) {
			t.Parallel()
			if _, err := BuildAnalysisPublication(context.Background(), input); err != nil {
				t.Error(err)
			}
		})
	}
}
