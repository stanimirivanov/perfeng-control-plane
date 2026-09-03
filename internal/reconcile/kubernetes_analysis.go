package reconcile

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// AnalysisJobResolver resolves an approved, digest-pinned normalization Job.
// The returned template must be reproducible from the immutable Run and input.
type AnalysisJobResolver interface {
	ResolveAnalysisJob(context.Context, string, run.Run, AnalysisInput) (*batchv1.Job, error)
}

// AnalysisJobController creates or adopts a deterministic normalization Job and
// returns its identity-checked Kubernetes phase.
type AnalysisJobController interface {
	EnsureAnalysisJob(context.Context, string, *batchv1.Job) (kubernetes.Observation, error)
}

// NormalizedArtifactCollector verifies the completed Job's output bytes and
// returns their immutable reference. Kubernetes success alone is insufficient.
type NormalizedArtifactCollector interface {
	CollectNormalizedArtifact(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error)
}

// KubernetesAnalysisExecutor connects the analysis lifecycle boundary to a
// deterministic Kubernetes Job and a separate output attestor.
type KubernetesAnalysisExecutor struct {
	resolver   AnalysisJobResolver
	controller AnalysisJobController
	collector  NormalizedArtifactCollector
}

var _ AnalysisExecutor = (*KubernetesAnalysisExecutor)(nil)
var _ AnalysisJobController = (*kubernetes.AnalysisDispatcher)(nil)

// NewKubernetesAnalysisExecutor validates the three trusted adapter boundaries.
func NewKubernetesAnalysisExecutor(
	resolver AnalysisJobResolver,
	controller AnalysisJobController,
	collector NormalizedArtifactCollector,
) (*KubernetesAnalysisExecutor, error) {
	if resolver == nil || controller == nil || collector == nil {
		return nil, run.ErrValidation
	}

	return &KubernetesAnalysisExecutor{
		resolver: resolver, controller: controller, collector: collector,
	}, nil
}

// Normalize ensures one normalization Job, waits for its terminal phase, then
// delegates byte verification to the output collector.
func (executor *KubernetesAnalysisExecutor) Normalize(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (run.Artifact, error) {
	template, err := executor.resolver.ResolveAnalysisJob(
		ctx,
		principal,
		current.Clone(),
		input.Clone(),
	)
	if err != nil {
		return run.Artifact{}, err
	}
	if template == nil {
		return run.Artifact{}, run.ErrValidation
	}

	observation, err := executor.controller.EnsureAnalysisJob(ctx, current.ID, template)
	if err != nil {
		return run.Artifact{}, err
	}
	switch observation.Phase {
	case kubernetes.JobPending, kubernetes.JobRunning:
		return run.Artifact{}, ErrAnalysisPending
	case kubernetes.JobFailed:
		return run.Artifact{}, ErrAnalysisFailed
	case kubernetes.JobSucceeded:
		return executor.collector.CollectNormalizedArtifact(
			ctx,
			principal,
			current.Clone(),
			input.Clone(),
		)
	default:
		return run.Artifact{}, run.ErrValidation
	}
}
