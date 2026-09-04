package kubernetes

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
)

const (
	analysisJobSuffix = "-analysis"
	analysisStage     = "analysis"
)

// AnalysisDispatcher creates or adopts one deterministic normalization Job per Run.
// It reports Kubernetes process state without interpreting normalized output.
type AnalysisDispatcher struct {
	stage *stageJobDispatcher
}

// NewAnalysisDispatcher binds normalization Jobs to one namespace.
func NewAnalysisDispatcher(jobs Jobs, namespace string) (*AnalysisDispatcher, error) {
	stage, err := newStageJobDispatcher(jobs, namespace, analysisJobSuffix, analysisStage)
	if err != nil {
		return nil, err
	}

	return &AnalysisDispatcher{stage: stage}, nil
}

// EnsureAnalysisJob creates the normalization Job or adopts an identical owned
// Job after a retry, then returns its current process phase.
func (dispatcher *AnalysisDispatcher) EnsureAnalysisJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (Observation, error) {
	return dispatcher.stage.ensure(ctx, runID, template)
}
