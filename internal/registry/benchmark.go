package registry

import (
	"context"
	"reflect"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ reconcile.BenchmarkResolver = (*ReportPolicyRegistry)(nil)

// HasBenchmarks requires explicit reservation-aware process composition when a benchmark is admitted.
func (registry *ReportPolicyRegistry) HasBenchmarks() bool {
	if registry == nil {
		return false
	}
	for _, entry := range registry.entries {
		if entry.benchmark != nil {
			return true
		}
	}
	return false
}

// ResolveBenchmark authorizes the same immutable principal/request context as ordinary execution.
// It remains usable during cancellation so persisted target identity is not lost after a state change.
func (registry *ReportPolicyRegistry) ResolveBenchmark(ctx context.Context, principal string, current run.Run) (reconcile.BenchmarkPlan, bool, error) {
	if err := ctx.Err(); err != nil {
		return reconcile.BenchmarkPlan{}, false, err
	}
	if registry == nil || principal == "" || current.Request.Validate() != nil {
		return reconcile.BenchmarkPlan{}, false, run.ErrValidation
	}
	entry, exists := registry.lookup(principal, current.Request)
	if !exists {
		return reconcile.BenchmarkPlan{}, false, run.ErrForbidden
	}
	if _, approved := entry.candidates[current.Request.Candidate.Image]; !approved {
		return reconcile.BenchmarkPlan{}, false, run.ErrForbidden
	}
	if entry.benchmark == nil {
		return reconcile.BenchmarkPlan{}, false, nil
	}
	return entry.benchmark.Clone(), true, nil
}

func cloneBenchmark(plan *reconcile.BenchmarkPlan) *reconcile.BenchmarkPlan {
	if plan == nil {
		return nil
	}
	clone := plan.Clone()
	return &clone
}

func validBenchmarkEntry(entry ReportPolicyEntry) bool {
	oci := entry.RawProducer.Name == "kube-burner" && entry.RawProducer.Artifact != nil && entry.RawProducer.Artifact.Kind == "oci-image"
	if entry.Benchmark == nil {
		return !oci
	}
	plan := entry.Benchmark
	return oci && entry.ContractsVersion == "0.22.0" && entry.RawProducer.Version == "2.8.5" && entry.NativeBaseline == nil &&
		entry.TestID == "pod-startup-k8s" && len(entry.CandidateImages) == 1 && entry.CandidateImages[0] == benchmark.CandidateImage &&
		entry.RawProducer.Artifact.Image == plan.Selection.RunnerImage && entry.Environment == plan.Selection.Environment &&
		entry.Workload == plan.Selection.Workload && plan.Validate() == nil && reflect.DeepEqual(entry.ExecutionTemplate, plan.Template)
}
