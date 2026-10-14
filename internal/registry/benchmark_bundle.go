package registry

import (
	"io/fs"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type benchmarkBundleConfig struct {
	ClusterID       string `json:"clusterId"`
	ClusterUID      string `json:"clusterUid"`
	EnvironmentFile string `json:"environmentFile"`
	TemplateSHA256  string `json:"templateSha256"`
	Bucket          string `json:"bucket"`
}

func loadBenchmarkPlan(source fs.FS, configured reportPolicyBundleEntry, template *batchv1.Job) (*reconcile.BenchmarkPlan, error) {
	if configured.Benchmark == nil {
		return nil, nil
	}
	if configured.RawProducer.Artifact == nil {
		return nil, run.ErrValidation
	}
	config := configured.Benchmark
	environmentBytes, err := readBundleFile(source, config.EnvironmentFile, maximumPolicyFileBytes)
	if err != nil {
		return nil, err
	}
	plan := &reconcile.BenchmarkPlan{
		Selection: benchmark.Spec{
			ClusterID: config.ClusterID, ClusterUID: config.ClusterUID, Environment: configured.Environment,
			EnvironmentBytes: environmentBytes, Workload: configured.Workload,
			TemplateSHA256: config.TemplateSHA256, RunnerImage: configured.RawProducer.Artifact.Image, Bucket: config.Bucket,
		},
		Template: cloneJob(template),
	}
	if plan.Validate() != nil {
		return nil, run.ErrValidation
	}
	return plan, nil
}
