package kubernetes

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
)

const (
	reportJobSuffix = "-report"
	reportingStage  = "reporting"
)

// ReportingDispatcher creates or adopts one deterministic report Job per Run.
// It reports Kubernetes process state without interpreting the report verdict.
type ReportingDispatcher struct {
	stage *stageJobDispatcher
}

// NewReportingDispatcher binds reporting Jobs to one namespace.
func NewReportingDispatcher(jobs Jobs, namespace string) (*ReportingDispatcher, error) {
	stage, err := newStageJobDispatcher(jobs, namespace, reportJobSuffix, reportingStage)
	if err != nil {
		return nil, err
	}

	return &ReportingDispatcher{stage: stage}, nil
}

// EnsureReportJob creates the report Job or adopts an identical owned Job
// after a retry, then returns its current process phase.
func (dispatcher *ReportingDispatcher) EnsureReportJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (Observation, error) {
	return dispatcher.stage.ensure(ctx, runID, template)
}
