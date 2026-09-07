package app

import (
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
)

// ExecutionTrust combines immutable normalization and reporting decisions.
// ReportPolicyRegistry satisfies this boundary.
type ExecutionTrust interface {
	reconcile.AnalysisPlanResolver
	reconcile.ReportTrustResolver
}

// KubernetesExecutorConfig contains reviewed normalization and reporting templates.
type KubernetesExecutorConfig struct {
	Analysis reconcile.AnalysisJobConfig
	Report   reconcile.ReportJobConfig
}

// KubernetesExecutors contains the analysis and reporting lifecycle adapters.
type KubernetesExecutors struct {
	Analysis  *reconcile.KubernetesAnalysisExecutor
	Reporting *reconcile.KubernetesReportExecutor
}

// NewKubernetesExecutors composes trusted plans, restricted Jobs, Kubernetes
// dispatch and output attestation without making external requests.
func NewKubernetesExecutors(
	trust ExecutionTrust,
	baselines baseline.Repository,
	controllers KubernetesControllers,
	collectors ArtifactCollectors,
	config KubernetesExecutorConfig,
) (KubernetesExecutors, error) {
	if trust == nil || baselines == nil || controllers.Analysis == nil ||
		controllers.Reporting == nil || collectors.Normalized == nil || collectors.Report == nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}

	analysisJobs, err := reconcile.NewKubernetesAnalysisJobResolver(trust, config.Analysis)
	if err != nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}
	analysis, err := reconcile.NewKubernetesAnalysisExecutor(
		analysisJobs,
		controllers.Analysis,
		collectors.Normalized,
	)
	if err != nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}
	reportPlans, err := reconcile.NewTrustedReportPlanResolver(baselines, trust)
	if err != nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}
	reportJobs, err := reconcile.NewKubernetesReportJobResolver(reportPlans, config.Report)
	if err != nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}
	reporting, err := reconcile.NewKubernetesReportExecutor(
		reportJobs,
		controllers.Reporting,
		collectors.Report,
	)
	if err != nil {
		return KubernetesExecutors{}, ErrInvalidConfiguration
	}

	return KubernetesExecutors{Analysis: analysis, Reporting: reporting}, nil
}
