package benchmark

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func testSelection(t *testing.T) Spec {
	t.Helper()
	descriptor := []byte(`{"cluster":"perfeng-local","fingerprint":"` + strings.Repeat("d", 64) + `","kubernetes":{"version":"v1.35.0","nodeCount":3,"nodePools":[{"name":"control-plane","cpuArchitecture":"amd64"},{"name":"performance-generator","cpuArchitecture":"amd64"},{"name":"sut","cpuArchitecture":"amd64"}]},"application":{"configurationHash":"` + ProfileSHA256 + `"}}`)
	return Spec{
		ClusterID: "perfeng-local", ClusterUID: "00000000-0000-4000-8000-000000000001", EnvironmentBytes: descriptor,
		Environment:    baseline.Environment{Identity: rawresult.Identity{ID: "local-kind-integration", Version: "1.0.0", SHA256: run.BenchmarkDigest(descriptor)}, Fingerprint: strings.Repeat("d", 64)},
		Workload:       rawresult.Identity{ID: "pod-startup-contained", Version: "1.0.0", SHA256: WorkloadSHA256},
		TemplateSHA256: TemplateSHA256, RunnerImage: "ghcr.io/stanimirivanov/perfeng-kube-burner@sha256:" + strings.Repeat("a", 64), Bucket: "perfeng-artifacts",
	}
}

func TestBenchmarkSelectionRejectsUnreviewedIdentityAndBytes(t *testing.T) {
	if run.BenchmarkDigest(profileBytes) != ProfileSHA256 {
		t.Fatal("embedded profile changed")
	}
	for name, mutate := range map[string]func(*Spec){
		"target":              func(spec *Spec) { spec.ClusterID = "other" },
		"cluster incarnation": func(spec *Spec) { spec.ClusterUID = "unknown" },
		"descriptor bytes":    func(spec *Spec) { spec.EnvironmentBytes = append(spec.EnvironmentBytes, ' ') },
		"workload source":     func(spec *Spec) { spec.Workload.SHA256 = strings.Repeat("b", 64) },
		"Pod source":          func(spec *Spec) { spec.TemplateSHA256 = strings.Repeat("b", 64) },
		"mutable executor":    func(spec *Spec) { spec.RunnerImage = "ghcr.io/stanimirivanov/perfeng-kube-burner:latest" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := testSelection(t)
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			mutate(&spec)
			if err := spec.Validate(); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("unreviewed selection accepted: %v", err)
			}
		})
	}
}

func TestBenchmarkRequestBindsClosedPackagedInputAndIsolatesDescriptor(t *testing.T) {
	spec := testSelection(t)
	clone := spec.Clone()
	clone.EnvironmentBytes[0] = 'x'
	if err := spec.Validate(); err != nil {
		t.Fatal("clone aliases descriptor")
	}
	const runID = "perf-20261014-120000-12345678"
	const namespaceUID = "00000000-0000-4000-8000-000000000002"
	content, err := RequestBytes(spec, runID, namespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(content, &request); err != nil {
		t.Fatal(err)
	}
	if len(request) != 12 {
		t.Fatalf("closed packaged request has %d fields", len(request))
	}
	for key, expected := range map[string]string{"runId": runID, "namespace": Namespace(runID), "namespaceUid": namespaceUID, "kubeSystemUid": spec.ClusterUID, "candidateImage": CandidateImage, "runnerImage": spec.RunnerImage, "templateSha256": TemplateSHA256} {
		var actual string
		if err := json.Unmarshal(request[key], &actual); err != nil || actual != expected {
			t.Fatalf("request %s binding differs", key)
		}
	}
	if _, err := RequestBytes(spec, "../foreign", namespaceUID); !errors.Is(err, run.ErrValidation) {
		t.Fatal("unsafe Run identity admitted")
	}
}
