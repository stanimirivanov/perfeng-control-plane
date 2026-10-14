package reconcile

import (
	"context"
	"encoding/json"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BenchmarkPlan combines trusted immutable selection with a reusable packaging template.
// It is internal composition data, never accepted from the public Run request.
type BenchmarkPlan struct {
	Selection benchmark.Spec `json:"selection"`
	Template  *batchv1.Job   `json:"template"`
}

type benchmarkIntentDocument struct {
	Version int           `json:"version"`
	Plan    BenchmarkPlan `json:"plan"`
}

// Clone isolates every template and descriptor byte from registry or caller mutation.
func (plan BenchmarkPlan) Clone() BenchmarkPlan {
	plan.Selection = plan.Selection.Clone()
	if plan.Template != nil {
		plan.Template = plan.Template.DeepCopy()
	}
	return plan
}

// Validate proves the narrow contained recipe without performing cluster or storage I/O.
func (plan BenchmarkPlan) Validate() error {
	if plan.Selection.Validate() != nil {
		return run.ErrValidation
	}
	return kubernetes.ValidateBenchmarkTemplate(plan.Template, plan.Selection.RunnerImage)
}

// Intent freezes the complete source plan before target-side effects; it is independent of observations.
func (plan BenchmarkPlan) Intent() (run.BenchmarkIntent, error) {
	if plan.Validate() != nil {
		return run.BenchmarkIntent{}, run.ErrValidation
	}
	data, err := json.Marshal(benchmarkIntentDocument{Version: 1, Plan: plan})
	if err != nil {
		return run.BenchmarkIntent{}, run.ErrValidation
	}
	intent := run.BenchmarkIntent{TargetID: plan.Selection.ClusterID, ClusterUID: plan.Selection.ClusterUID, Plan: data, PlanSHA256: run.BenchmarkDigest(data)}
	if !intent.Valid() {
		return run.BenchmarkIntent{}, run.ErrValidation
	}
	return intent, nil
}

// RestoreBenchmarkPlan verifies sealed source identity before ambiguous-create or cancellation recovery.
func RestoreBenchmarkPlan(intent run.BenchmarkIntent) (BenchmarkPlan, error) {
	var document benchmarkIntentDocument
	if !intent.Valid() || !jsondocument.Valid(intent.Plan, 1<<20) || !jsondocument.Decode(intent.Plan, &document) || document.Version != 1 ||
		document.Plan.Validate() != nil || document.Plan.Selection.ClusterID != intent.TargetID || document.Plan.Selection.ClusterUID != intent.ClusterUID {
		return BenchmarkPlan{}, run.ErrValidation
	}
	return document.Plan, nil
}

// BenchmarkResolver identifies explicitly admitted benchmark Runs independently of their mutable state.
type BenchmarkResolver interface {
	// HasBenchmarks reports whether startup entries require the reservation-aware execution path.
	HasBenchmarks() bool
	// ResolveBenchmark returns an authorized isolated plan and true, or false for a non-benchmark entry.
	// Forbidden, malformed selection and dependency errors must not be interpreted as ordinary execution.
	ResolveBenchmark(context.Context, string, run.Run) (BenchmarkPlan, bool, error)
}
