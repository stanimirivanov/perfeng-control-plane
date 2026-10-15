package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func benchmarkPolicyFixture(t *testing.T) (ReportPolicyEntry, run.Run) {
	t.Helper()
	entry, current := v2RawPolicyFixture(t, "oci-image")
	descriptor := []byte(`{"cluster":"perfeng-local","fingerprint":"` + strings.Repeat("d", 64) + `","kubernetes":{"version":"v1.35.0","nodeCount":3,"nodePools":[{"name":"control-plane","cpuArchitecture":"amd64"},{"name":"performance-generator","cpuArchitecture":"amd64"},{"name":"sut","cpuArchitecture":"amd64"}]},"application":{"configurationHash":"` + benchmark.ProfileSHA256 + `"}}`)
	entry.ContractsVersion = "0.22.0"
	entry.TestID = "pod-startup-k8s"
	entry.Environment = baseline.Environment{Identity: rawresult.Identity{ID: "local-kind-integration", Version: "1.0.0", SHA256: run.BenchmarkDigest(descriptor)}, Fingerprint: strings.Repeat("d", 64)}
	entry.Workload = rawresult.Identity{ID: "pod-startup-contained", Version: "1.0.0", SHA256: benchmark.WorkloadSHA256}
	entry.CandidateImages = []string{benchmark.CandidateImage}
	image := "ghcr.io/stanimirivanov/perfeng-kube-burner@sha256:" + strings.Repeat("a", 64)
	entry.RawProducer = rawresult.Producer{Name: "kube-burner", Version: "2.8.5", Artifact: &rawresult.ExecutionArtifact{Kind: "oci-image", Image: image}}
	entry.Normalization = reconcile.NormalizationSpec{SourceFormat: reconcile.KubeBurnerPodLatencyFormat}
	entry.ExecutionTemplate = kubernetes.BenchmarkJobTemplate(image, "registry", "storage", "http://seaweedfs.perf-platform.svc.cluster.local:8333")
	entry.Benchmark = &reconcile.BenchmarkPlan{Selection: benchmark.Spec{
		ClusterID: "perfeng-local", ClusterUID: "00000000-0000-4000-8000-000000000001", Environment: entry.Environment, EnvironmentBytes: descriptor,
		Workload: entry.Workload, TemplateSHA256: benchmark.TemplateSHA256, RunnerImage: image, Bucket: "perfeng-artifacts",
	}, Template: entry.ExecutionTemplate.DeepCopy()}
	current.Request.TestSuite = entry.TestID
	current.Request.Candidate.Image = benchmark.CandidateImage
	current.Request.Environment = run.Reference{ID: entry.Environment.ID, Version: entry.Environment.Version, SHA256: entry.Environment.SHA256}
	current.State = run.StateProvisioning
	return entry, current
}

func TestBenchmarkRegistryAuthorizesAndIsolatesReviewedPlan(t *testing.T) {
	entry, current := benchmarkPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if !registry.HasBenchmarks() {
		t.Fatal("benchmark composition requirement missing")
	}
	if target, err := registry.ApproveRun(context.Background(), "alice", current.Request); err != nil || target != run.ExecutionTargetKubernetes {
		t.Fatalf("approval: %v", err)
	}
	plan, selected, err := registry.ResolveBenchmark(context.Background(), "alice", current)
	if err != nil || !selected {
		t.Fatalf("resolution: %v", err)
	}
	entry.Benchmark.Selection.EnvironmentBytes[0] = 'x'
	plan.Template.Spec.Template.Spec.HostNetwork = true
	again, selected, err := registry.ResolveBenchmark(context.Background(), "alice", current)
	if err != nil || !selected || again.Validate() != nil {
		t.Fatal("registry aliases source/resolution")
	}
	if _, err := registry.ResolveJob(context.Background(), "alice", current); !errors.Is(err, run.ErrForbidden) {
		t.Fatalf("generic dispatch bypass allowed: %v", err)
	}
	current.State = run.StateValidating
	if _, err := registry.ResolveJob(context.Background(), "alice", current); err != nil {
		t.Fatal(err)
	}
	if _, _, err := registry.ResolveBenchmark(context.Background(), "other", current); !errors.Is(err, run.ErrForbidden) {
		t.Fatalf("principal not fenced: %v", err)
	}
	current.Request.Candidate.Image = "ghcr.io/example/unapproved@sha256:" + strings.Repeat("f", 64)
	if _, _, err := registry.ResolveBenchmark(context.Background(), "alice", current); !errors.Is(err, run.ErrForbidden) {
		t.Fatalf("candidate not fenced: %v", err)
	}
}

func TestBenchmarkRegistryRejectsUnreservedOrDriftingOCIRunner(t *testing.T) {
	for name, mutate := range map[string]func(*ReportPolicyEntry){
		"missing admission": func(entry *ReportPolicyEntry) { entry.Benchmark = nil },
		"wrong workload":    func(entry *ReportPolicyEntry) { entry.Workload.SHA256 = strings.Repeat("b", 64) },
		"different candidate": func(entry *ReportPolicyEntry) {
			entry.CandidateImages = []string{"ghcr.io/example/unsafe@sha256:" + strings.Repeat("b", 64)}
		},
		"privilege": func(entry *ReportPolicyEntry) { entry.Benchmark.Template.Spec.Template.Spec.HostPID = true },
		"different template": func(entry *ReportPolicyEntry) {
			entry.ExecutionTemplate.Spec.Template.Spec.ServiceAccountName = "another"
		},
		"environment bytes": func(entry *ReportPolicyEntry) {
			entry.Benchmark.Selection.EnvironmentBytes = append(entry.Benchmark.Selection.EnvironmentBytes, ' ')
		},
	} {
		t.Run(name, func(t *testing.T) {
			entry, _ := benchmarkPolicyFixture(t)
			mutate(&entry)
			if _, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry}); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("unsafe selection accepted: %v", err)
			}
		})
	}
}

func TestBenchmarkBundleLoadsExactDescriptorWithoutNewRunFields(t *testing.T) {
	entry, current := benchmarkPolicyFixture(t)
	source, _, _ := registryBundleFixture(t)
	var bundle reportPolicyBundle
	if err := json.Unmarshal(source[registryManifestPath].Data, &bundle); err != nil {
		t.Fatal(err)
	}
	configured := &bundle.Entries[0]
	configured.TestID = entry.TestID
	configured.ContractsVersion = entry.ContractsVersion
	configured.RawProducer = entry.RawProducer
	configured.Workload = entry.Workload
	configured.Environment = entry.Environment
	configured.CandidateImages = entry.CandidateImages
	configured.Normalization = normalizationBundleConfig{SourceFormat: entry.Normalization.SourceFormat}
	configured.Benchmark = &benchmarkBundleConfig{ClusterID: entry.Benchmark.Selection.ClusterID, ClusterUID: entry.Benchmark.Selection.ClusterUID, EnvironmentFile: "environments/local.json", TemplateSHA256: benchmark.TemplateSHA256, Bucket: "perfeng-artifacts"}
	var err error
	source[registryManifestPath].Data, err = json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	source[registryTemplatePath].Data, err = json.Marshal(entry.ExecutionTemplate)
	if err != nil {
		t.Fatal(err)
	}
	source["environments/local.json"] = &fstest.MapFile{Data: entry.Benchmark.Selection.EnvironmentBytes}
	registry, err := LoadReportPolicyRegistry(source, registryManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, selected, err := registry.ResolveBenchmark(context.Background(), "alice", current)
	if err != nil || !selected || string(plan.Selection.EnvironmentBytes) != string(entry.Benchmark.Selection.EnvironmentBytes) {
		t.Fatalf("exact bytes not loaded: %v", err)
	}
	source["environments/local.json"].Data = append(source["environments/local.json"].Data, ' ')
	if _, err := LoadReportPolicyRegistry(source, registryManifestPath); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("descriptor hash drift accepted: %v", err)
	}
}
