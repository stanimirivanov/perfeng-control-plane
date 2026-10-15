package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestMixedBundleStartupRequiresExplicitRegistryMode(t *testing.T) {
	config, _, currents := mixedBundleFixture(t)
	for _, version := range []string{"0.13.0", "0.22.0", RegistryContractsVersion} {
		t.Run(version, func(t *testing.T) {
			loaded, err := NewRuntimeRegistry(config, ArtifactVerificationConfig{ContractsVersion: version})
			if version != RegistryContractsVersion {
				if !errors.Is(err, ErrInvalidConfiguration) || loaded != nil {
					t.Fatalf("uniform mode accepted mixed bundle: %v", err)
				}
				return
			}
			if err != nil || !loaded.HasBenchmarks() {
				t.Fatalf("mixed registry startup = %v", err)
			}
			for _, current := range currents {
				if _, err := loaded.ApproveRun(context.Background(), "operator", current.Request); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRegistryModeStillRejectsInvalidBundleAtStartup(t *testing.T) {
	for name, corrupt := range map[string]func(string) string{
		"empty": func(string) string { return `{"schemaVersion":1,"entries":[]}` },
		"unknown field": func(value string) string {
			return strings.Replace(value, `"entries":`, `"unexpected":true,"entries":`, 1)
		},
		"duplicate field": func(value string) string {
			return strings.Replace(value, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)
		},
		"invalid entry version": func(value string) string {
			return strings.Replace(value, `"contractsVersion":"0.13.0"`, `"contractsVersion":"latest"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			config, _, _ := mixedBundleFixture(t)
			path := filepath.Join(config.RootDirectory, config.Manifest)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(corrupt(string(data))), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRuntimeRegistry(config, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion}); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("invalid bundle accepted: %v", err)
			}
		})
	}
}

func TestUniformCollectorConstraintCannotBeBypassedBySeparateComposition(t *testing.T) {
	config, _, currents := mixedBundleFixture(t)
	loaded, err := NewRuntimeRegistry(config, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
	if err != nil {
		t.Fatal(err)
	}
	collectors, err := NewArtifactCollectors(inertArtifactStorage{}, loaded, inertBaselineRepository{},
		ArtifactVerificationConfig{ContractsVersion: "0.13.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collectors.Raw.CollectRawArtifacts(context.Background(), "operator", currents[1]); !errors.Is(err, reconcile.ErrInvalidArtifacts) {
		t.Fatalf("collector ignored uniform constraint: %v", err)
	}
}

func TestMixedBundleCollectorsVerifyEachEntryAndRejectCrossVersionClaims(t *testing.T) {
	config, entries, currents := mixedBundleFixture(t)
	loaded, err := NewRuntimeRegistry(config, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range entries {
		t.Run(entry.TestID, func(t *testing.T) {
			t.Parallel()
			for _, changed := range []string{"none", "raw", "normalized", "report", "raw-unknown", "normalized-unknown", "report-unknown"} {
				t.Run(changed, func(t *testing.T) {
					storage := newMixedEvidence(t, entry, currents[index], changed)
					collectors, err := NewArtifactCollectors(storage, loaded, missingMixedBaseline{},
						ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
					if err != nil {
						t.Fatal(err)
					}
					current := currents[index]
					current.State = run.StateCollecting
					_, err = collectors.Raw.CollectRawArtifacts(context.Background(), "operator", current)
					if strings.HasPrefix(changed, "raw") {
						if !errors.Is(err, reconcile.ErrInvalidArtifacts) {
							t.Fatalf("wrong raw contracts accepted: %v", err)
						}
					} else if err != nil {
						t.Fatalf("raw verification: %v", err)
					}
					current.State = run.StateAnalyzing
					_, err = collectors.Normalized.CollectNormalizedArtifact(context.Background(), "operator", current,
						reconcile.AnalysisInput{Manifest: storage.raw, Sources: storage.sources})
					if strings.HasPrefix(changed, "raw") || strings.HasPrefix(changed, "normalized") {
						if !errors.Is(err, reconcile.ErrAnalysisFailed) {
							t.Fatalf("wrong normalized/source contracts accepted: %v", err)
						}
					} else if err != nil {
						t.Fatalf("normalized verification: %v", err)
					}
					current.State = run.StateReporting
					_, err = collectors.Report.CollectReportArtifact(context.Background(), "operator", current,
						reconcile.ReportingInput{Candidate: storage.normalized})
					if strings.HasPrefix(changed, "report") {
						if !errors.Is(err, reconcile.ErrReportFailed) {
							t.Fatalf("wrong report contracts accepted: %v", err)
						}
					} else if err != nil {
						t.Fatalf("report verification: %v", err)
					}
				})
			}
		})
	}
}

func mixedBundleFixture(t *testing.T) (RegistryConfig, []registry.ReportPolicyEntry, []run.Run) {
	t.Helper()
	root := t.TempDir()
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	analysis := rawresult.Producer{Name: "perfeng-analysis", Version: "0.8.1",
		Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("a", 64)}
	k6Image := "ghcr.io/example/k6@sha256:" + strings.Repeat("b", 64)
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false
	checkout := registry.ReportPolicyEntry{
		PolicyBytes: policyBytes, TestID: "checkout-api", ContractsVersion: "0.13.0",
		Catalogue: run.Reference{ID: "platform-tests", Version: "1.2.0", SHA256: strings.Repeat("c", 64)},
		Profile:   "smoke", RawProducer: rawresult.Producer{Name: "k6", Version: "2.2.0",
			Artifact: &rawresult.ExecutionArtifact{Kind: "oci-image", Image: k6Image}},
		NormalizerProducer: analysis, ReportProducer: analysis,
		Normalization: reconcile.NormalizationSpec{SourceFormat: reconcile.K6SummaryFormat, Layout: reconcile.K6TypedLayout},
		Workload:      rawresult.Identity{ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("d", 64)},
		Environment: baseline.Environment{Identity: rawresult.Identity{
			ID: "local-kind-windows", Version: "1.0.0", SHA256: strings.Repeat("e", 64)}, Fingerprint: strings.Repeat("f", 64)},
		Dataset: baseline.Dataset{Kind: "none"}, Principals: []string{"operator"},
		CandidateImages: []string{benchmark.CandidateImage},
		ExecutionTemplate: &batchv1.Job{Spec: batchv1.JobSpec{
			BackoffLimit: &zero, Completions: &one, Parallelism: &one, ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
				AutomountServiceAccountToken: &automount, Containers: []corev1.Container{{Name: "runner", Image: k6Image}}}},
		}},
	}
	contained := checkout
	contained.TestID, contained.ContractsVersion = "pod-startup-k8s", "0.22.0"
	descriptor := []byte(`{"cluster":"perfeng-local","fingerprint":"` + strings.Repeat("f", 64) + `","kubernetes":{"version":"v1.35.0","nodeCount":3,"nodePools":[{"name":"control-plane","cpuArchitecture":"amd64"},{"name":"performance-generator","cpuArchitecture":"amd64"},{"name":"sut","cpuArchitecture":"amd64"}]},"application":{"configurationHash":"` + benchmark.ProfileSHA256 + `"}}`)
	contained.Environment = baseline.Environment{Identity: rawresult.Identity{ID: "local-kind-integration",
		Version: "1.0.0", SHA256: run.BenchmarkDigest(descriptor)}, Fingerprint: strings.Repeat("f", 64)}
	contained.Workload = rawresult.Identity{ID: "pod-startup-contained", Version: "1.0.0", SHA256: benchmark.WorkloadSHA256}
	image := "ghcr.io/stanimirivanov/perfeng-kube-burner@sha256:" + strings.Repeat("b", 64)
	contained.RawProducer = rawresult.Producer{Name: "kube-burner", Version: "2.8.5",
		Artifact: &rawresult.ExecutionArtifact{Kind: "oci-image", Image: image}}
	contained.Normalization = reconcile.NormalizationSpec{SourceFormat: reconcile.KubeBurnerPodLatencyFormat}
	contained.ExecutionTemplate = kubernetes.BenchmarkJobTemplate(image, "registry", "storage", "http://seaweedfs.perf-platform.svc.cluster.local:8333")
	contained.Benchmark = &reconcile.BenchmarkPlan{Selection: benchmark.Spec{ClusterID: "perfeng-local",
		ClusterUID: "00000000-0000-4000-8000-000000000001", Environment: contained.Environment, EnvironmentBytes: descriptor,
		Workload: contained.Workload, TemplateSHA256: benchmark.TemplateSHA256, RunnerImage: image, Bucket: "perfeng-artifacts"},
		Template: contained.ExecutionTemplate.DeepCopy()}
	entries := []registry.ReportPolicyEntry{checkout, contained}
	for _, entry := range entries {
		if _, err := registry.NewReportPolicyRegistry([]registry.ReportPolicyEntry{entry}); err != nil {
			t.Fatalf("invalid %s fixture: %v", entry.TestID, err)
		}
	}
	var documents []map[string]any
	var currents []run.Run
	for index, entry := range entries {
		templatePath := fmt.Sprintf("job-%d.json", index)
		writeMixedJSON(t, filepath.Join(root, templatePath), entry.ExecutionTemplate)
		document := map[string]any{
			"policy": "policy.json", "testId": entry.TestID, "contractsVersion": entry.ContractsVersion,
			"catalogue": entry.Catalogue, "profile": entry.Profile, "rawProducer": entry.RawProducer,
			"normalizerProducer": entry.NormalizerProducer, "reportProducer": entry.ReportProducer,
			"normalization":     map[string]string{"sourceFormat": entry.Normalization.SourceFormat, "layout": entry.Normalization.Layout},
			"executionTemplate": templatePath, "workload": entry.Workload, "environment": entry.Environment,
			"dataset": entry.Dataset, "candidateImages": entry.CandidateImages, "principals": entry.Principals,
		}
		if entry.Benchmark != nil {
			document["benchmark"] = map[string]string{"clusterId": "perfeng-local",
				"clusterUid": entry.Benchmark.Selection.ClusterUID, "environmentFile": "environment.json",
				"templateSha256": benchmark.TemplateSHA256, "bucket": "perfeng-artifacts"}
		}
		documents = append(documents, document)
		currents = append(currents, run.Run{ID: fmt.Sprintf("perf-20261015-120000-1234567%d", index),
			State: run.StateCollecting, Revision: 7, Request: run.Request{
				TestSuite: entry.TestID, Catalogue: entry.Catalogue, Profile: entry.Profile,
				Candidate:   run.Candidate{GitSHA: strings.Repeat("1", 40), Image: entry.CandidateImages[0]},
				Environment: run.Reference{ID: entry.Environment.ID, Version: entry.Environment.Version, SHA256: entry.Environment.SHA256},
				Policy:      run.Reference{ID: "search-browser", Version: "1.0.0", SHA256: run.BenchmarkDigest(policyBytes)},
			}})
	}
	for name, data := range map[string][]byte{"policy.json": policyBytes, "environment.json": descriptor} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMixedJSON(t, filepath.Join(root, "registry.json"), map[string]any{"schemaVersion": 1, "entries": documents})
	return RegistryConfig{RootDirectory: root, Manifest: "registry.json"}, entries, currents
}

func writeMixedJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type missingMixedBaseline struct{ inertBaselineRepository }

func (missingMixedBaseline) ResolveApprovedBaseline(context.Context, string, baseline.Selection) (baseline.Record, bool, error) {
	return baseline.Record{}, false, nil
}

type mixedEvidence struct {
	raw, normalized, report run.Artifact
	sources                 []run.Artifact
	objects                 map[run.Artifact][]byte
}

func (storage *mixedEvidence) ResolveRawManifest(context.Context, string, run.Run) (run.Artifact, error) {
	return storage.raw, nil
}

func (storage *mixedEvidence) ResolveNormalizedManifest(context.Context, string, run.Run) (run.Artifact, error) {
	return storage.normalized, nil
}

func (storage *mixedEvidence) ResolveReportManifest(context.Context, string, run.Run) (run.Artifact, error) {
	return storage.report, nil
}

func (storage *mixedEvidence) Read(_ context.Context, reference run.Artifact) ([]byte, error) {
	data, exists := storage.objects[reference]
	digest := sha256.Sum256(data)
	if !exists || reference.SizeBytes != int64(len(data)) || reference.SHA256 != hex.EncodeToString(digest[:]) {
		return nil, run.ErrValidation
	}
	return append([]byte(nil), data...), nil
}

func newMixedEvidence(t *testing.T, entry registry.ReportPolicyEntry, current run.Run, changed string) *mixedEvidence {
	t.Helper()
	storage := &mixedEvidence{objects: make(map[run.Artifact][]byte)}
	put := func(id, key, kind, format string, value any) run.Artifact {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reference := run.Artifact{ID: id, RunID: current.ID, Kind: kind,
			URI: "s3://perfeng-artifacts/runs/" + current.ID + "/" + key, SizeBytes: int64(len(data)),
			SHA256: run.BenchmarkDigest(data), MediaType: "application/json", Format: format}
		storage.objects[reference] = data
		return reference
	}
	storage.sources = []run.Artifact{put("11111111-1111-4111-8111-111111111111", "source.json", "raw", entry.Normalization.SourceFormat, map[string]any{})}
	version := func(stage string) string {
		if !strings.HasPrefix(changed, stage) {
			return entry.ContractsVersion
		}
		if strings.HasSuffix(changed, "-unknown") {
			return "9.9.9"
		}
		if entry.ContractsVersion == "0.13.0" {
			return "0.22.0"
		}
		return "0.13.0"
	}
	window := rawresult.Window{Start: "2026-10-15T12:00:00Z", End: "2026-10-15T12:01:00Z"}
	storage.raw = put("22222222-2222-4222-8222-222222222222", "raw-result.json", "raw", "raw-result/v2", rawresult.Manifest{
		SchemaVersion: 2, Kind: "RawResult", ContractsVersion: version("raw"), RunID: current.ID,
		TestID: entry.TestID, Workload: entry.Workload, Producer: entry.RawProducer, MeasurementWindow: window,
		CreatedAt: "2026-10-15T12:01:01Z", Artifacts: storage.sources})
	storage.normalized = put("33333333-3333-4333-8333-333333333333", "normalized-result.json", "normalized", "normalized-result/v1", normalizedresult.Manifest{
		SchemaVersion: 1, Kind: "NormalizedResult", ContractsVersion: version("normalized"), RunID: current.ID,
		TestID: entry.TestID, Workload: entry.Workload, Producer: entry.NormalizerProducer, MeasurementWindow: window,
		CreatedAt: "2026-10-15T12:01:02Z", SourceArtifacts: storage.sources,
		Results: []normalizedresult.Result{{SchemaVersion: 2, RunID: current.ID,
			Metric:       normalizedresult.Metric{Name: "ui.search.action_to_visible_ms", Direction: "lower-is-better"},
			Distribution: normalizedresult.Distribution{}}}})
	value, samples := float64(180), int64(20)
	storage.report = put("44444444-4444-4444-8444-444444444444", "analysis-result.json", "normalized", "analysis-result/v1", analysisresult.Manifest{
		SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: version("report"), RunID: current.ID,
		TestID: entry.TestID, CreatedAt: "2026-10-15T12:01:03Z", Producer: entry.ReportProducer,
		Policy: analysisresult.Policy{ID: current.Request.Policy.ID, Version: current.Request.Policy.Version,
			SHA256: current.Request.Policy.SHA256, Mode: "inform"}, CandidateArtifact: storage.normalized,
		ReferenceArtifacts: []run.Artifact{}, Evaluations: []analysisresult.Evaluation{{RuleID: "search-latency",
			Metric:     analysisresult.Metric{Name: "ui.search.action_to_visible_ms", Statistic: "mean", Unit: "ms"},
			Quality:    analysisresult.Quality{Status: "PASS", Reasons: []string{}, Samples: &samples},
			SLO:        analysisresult.SLO{Status: "PASS", Reasons: []string{}, Value: &value},
			Regression: analysisresult.Regression{Status: "INCONCLUSIVE", Reasons: []string{"No approved baseline is available."}, CandidateValue: &value},
		}}})
	return storage
}
