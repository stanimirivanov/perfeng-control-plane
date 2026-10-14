// Package benchmark owns immutable admission and input semantics for contained Kubernetes benchmarks.
package benchmark

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	// ProfileSHA256 identifies the unchanged environment-owned local integration policy.
	ProfileSHA256 = "b9fd1c55816ebb73b7b1ead490d53f4d53dae966be5e93b1bc45c9e0fc8489f5"
	// NodeImage is the reviewed kind node distribution bound into the environment fingerprint.
	NodeImage = "kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f"
	// CandidateImage is the warm pause image actually measured by this first contained workload.
	CandidateImage = "registry.k8s.io/pause@sha256:278fb9dbcca9518083ad1e11276933a2e96f23de604a3a08cc3c80002767d24c"
	// WorkloadSHA256 binds the packaged workload rather than an arbitrary same-name recipe.
	WorkloadSHA256 = "1dd90543d8cbd79014fbb522890463c0ee0c324d0ecff92973da58423500a29d"
	// TemplateSHA256 binds the exact contained Pod source in perfeng-k8s PR3.
	TemplateSHA256 = "6a5ee42c2d092d39e68869dcef7ab3ea04b28e8908c2decbe059d3fb2cd89982"
)

// ErrNotQualified means a required actual target observation failed or is unknown. It grants no authority.
var ErrNotQualified = errors.New("benchmark target qualification failed or is unknown")

//go:embed profile.json
var profileBytes []byte

var runnerPattern = regexp.MustCompile(`^ghcr\.io/stanimirivanov/perfeng-kube-burner@sha256:[a-f0-9]{64}$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Spec is reviewed Run-independent selection. It contains no credentials, endpoint or caller-supplied command.
type Spec struct {
	ClusterID        string               `json:"clusterId"`
	ClusterUID       string               `json:"clusterUid"`
	Environment      baseline.Environment `json:"environment"`
	EnvironmentBytes []byte               `json:"environmentBytes"`
	Workload         rawresult.Identity   `json:"workload"`
	TemplateSHA256   string               `json:"templateSha256"`
	RunnerImage      string               `json:"runnerImage"`
	Bucket           string               `json:"bucket"`
}

// Validate checks immutable source bindings and limits this slice to the reviewed local integration cohort.
func (spec Spec) Validate() error {
	if spec.ClusterID != "perfeng-local" || !run.ValidBenchmarkUID(spec.ClusterUID) ||
		spec.Environment.Validate() != nil || spec.Environment.ID != "local-kind-integration" ||
		spec.Environment.Version != "1.0.0" || len(spec.EnvironmentBytes) == 0 || len(spec.EnvironmentBytes) > 64<<10 ||
		run.BenchmarkDigest(spec.EnvironmentBytes) != spec.Environment.SHA256 ||
		run.BenchmarkDigest(profileBytes) != ProfileSHA256 || spec.Workload.Validate() != nil ||
		spec.Workload.ID != "pod-startup-contained" || spec.Workload.Version != "1.0.0" || spec.Workload.SHA256 != WorkloadSHA256 ||
		spec.TemplateSHA256 != TemplateSHA256 || !runnerPattern.MatchString(spec.RunnerImage) || !bucketPattern.MatchString(spec.Bucket) {
		return run.ErrValidation
	}
	var environment struct {
		Cluster     string `json:"cluster"`
		Fingerprint string `json:"fingerprint"`
		Kubernetes  struct {
			Version   string `json:"version"`
			NodeCount int    `json:"nodeCount"`
			NodePools []struct {
				Name         string `json:"name"`
				Architecture string `json:"cpuArchitecture"`
			} `json:"nodePools"`
		} `json:"kubernetes"`
		Application struct {
			ConfigurationHash string `json:"configurationHash"`
		} `json:"application"`
	}
	if !jsondocument.Valid(spec.EnvironmentBytes, 1<<20) || !jsondocument.Decode(spec.EnvironmentBytes, &environment) ||
		environment.Cluster != spec.ClusterID || environment.Fingerprint != spec.Environment.Fingerprint ||
		environment.Kubernetes.Version != "v1.35.0" || environment.Kubernetes.NodeCount != 3 || len(environment.Kubernetes.NodePools) != 3 ||
		environment.Application.ConfigurationHash != ProfileSHA256 {
		return run.ErrValidation
	}
	roles := map[string]bool{}
	for _, pool := range environment.Kubernetes.NodePools {
		if pool.Architecture != "amd64" || roles[pool.Name] {
			return run.ErrValidation
		}
		roles[pool.Name] = true
	}
	if !roles["control-plane"] || !roles["performance-generator"] || !roles["sut"] {
		return run.ErrValidation
	}
	return nil
}

// Clone isolates the environment bytes retained by trusted runtime selection.
func (spec Spec) Clone() Spec {
	spec.EnvironmentBytes = bytes.Clone(spec.EnvironmentBytes)
	return spec
}

// Namespace gives every Run a separate target namespace without granting creation authority.
func Namespace(runID string) string { return "perfeng-benchmark-" + runID }

// ResourceName identifies per-Run credentials, bindings and immutable inputs in the executor namespace.
func ResourceName(runID string) string { return runID + "-benchmark" }

// RequestBytes constructs the exact closed request consumed by the packaged adapter after UID binding.
func RequestBytes(spec Spec, runID, namespaceUID string) ([]byte, error) {
	if spec.Validate() != nil || !contract.ValidID(runID) || len(Namespace(runID)) > 63 || !run.ValidBenchmarkUID(namespaceUID) {
		return nil, run.ErrValidation
	}
	value := map[string]any{
		"schemaVersion": 1, "runId": runID, "testId": "pod-startup-k8s",
		"runnerImage": spec.RunnerImage, "candidateImage": CandidateImage, "workload": spec.Workload,
		"templateSha256": spec.TemplateSHA256, "namespace": Namespace(runID), "namespaceUid": namespaceUID,
		"kubeSystemUid": spec.ClusterUID, "environment": spec.Environment, "bucket": spec.Bucket,
	}
	return json.Marshal(value)
}
