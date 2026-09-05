package reconcile

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportJobResolver resolves an approved, digest-pinned reporting Job. The
// returned template must be reproducible from the immutable Run and candidate.
type ReportJobResolver interface {
	// ResolveReportJob returns an independently owned template for the principal,
	// current Run and exact normalized candidate.
	ResolveReportJob(context.Context, string, run.Run, ReportingInput) (*batchv1.Job, error)
}

// ReportJobController creates or adopts a deterministic reporting Job and
// returns its identity-checked Kubernetes phase.
type ReportJobController interface {
	// EnsureReportJob creates or adopts the deterministic reporting Job and
	// returns its identity-checked current phase.
	EnsureReportJob(context.Context, string, *batchv1.Job) (kubernetes.Observation, error)
}

// ReportArtifactCollector verifies the completed Job's output bytes and
// returns their immutable reference. Kubernetes success alone is insufficient.
type ReportArtifactCollector interface {
	// CollectReportArtifact verifies and returns the completed Job's exact report.
	CollectReportArtifact(context.Context, string, run.Run, ReportingInput) (run.Artifact, error)
}

// KubernetesReportExecutor connects the reporting lifecycle boundary to a
// deterministic Kubernetes Job and a separate output attestor.
type KubernetesReportExecutor struct {
	resolver   ReportJobResolver
	controller ReportJobController
	collector  ReportArtifactCollector
}

var _ ReportExecutor = (*KubernetesReportExecutor)(nil)
var _ ReportJobController = (*kubernetes.ReportingDispatcher)(nil)

// NewKubernetesReportExecutor validates the three trusted adapter boundaries.
func NewKubernetesReportExecutor(
	resolver ReportJobResolver,
	controller ReportJobController,
	collector ReportArtifactCollector,
) (*KubernetesReportExecutor, error) {
	if resolver == nil || controller == nil || collector == nil {
		return nil, run.ErrValidation
	}

	return &KubernetesReportExecutor{
		resolver: resolver, controller: controller, collector: collector,
	}, nil
}

// Report ensures one reporting Job, waits for its terminal phase, then
// delegates byte verification to the output collector.
func (executor *KubernetesReportExecutor) Report(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (run.Artifact, error) {
	template, err := executor.resolver.ResolveReportJob(
		ctx,
		principal,
		current.Clone(),
		input,
	)
	if err != nil {
		return run.Artifact{}, err
	}
	if template == nil {
		return run.Artifact{}, run.ErrValidation
	}

	observation, err := executor.controller.EnsureReportJob(ctx, current.ID, template)
	if err != nil {
		return run.Artifact{}, err
	}
	switch observation.Phase {
	case kubernetes.JobPending, kubernetes.JobRunning:
		return run.Artifact{}, ErrReportPending
	case kubernetes.JobFailed:
		return run.Artifact{}, ErrReportFailed
	case kubernetes.JobSucceeded:
		return executor.collector.CollectReportArtifact(
			ctx,
			principal,
			current.Clone(),
			input,
		)
	default:
		return run.Artifact{}, run.ErrValidation
	}
}
