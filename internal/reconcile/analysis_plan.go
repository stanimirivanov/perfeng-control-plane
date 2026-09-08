package reconcile

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	// K6SummaryFormat identifies the aggregate k6 input accepted by perfeng-analysis.
	K6SummaryFormat = "k6-summary-json"
	// PlaywrightMeasurementsV2Format identifies semantic browser duration measurements.
	PlaywrightMeasurementsV2Format = "playwright-measurements/v2"
	// KubeBurnerPodLatencyFormat identifies individual pod-startup observations.
	KubeBurnerPodLatencyFormat = "kube-burner-pod-latency-json"
	// K6TypedLayout identifies the typed k6 summary representation.
	K6TypedLayout = "typed"
	// K6LegacyExportLayout identifies the flat k6 summary-export representation.
	K6LegacyExportLayout = "legacy-export"
)

// NormalizationSpec selects one supported raw format and its explicit input layout.
type NormalizationSpec struct {
	SourceFormat string
	Layout       string
}

// Validate rejects unsupported formats and ambiguous tool-specific options.
func (spec NormalizationSpec) Validate() error {
	switch spec.SourceFormat {
	case K6SummaryFormat:
		if spec.Layout != K6TypedLayout && spec.Layout != K6LegacyExportLayout {
			return run.ErrValidation
		}
	case PlaywrightMeasurementsV2Format, KubeBurnerPodLatencyFormat:
		if spec.Layout != "" {
			return run.ErrValidation
		}
	default:
		return run.ErrValidation
	}

	return nil
}

// AnalysisPlan binds one verified source to an approved normalization runtime.
// It contains no storage credentials, local paths or Kubernetes configuration.
type AnalysisPlan struct {
	Source   run.Artifact
	Producer rawresult.Producer
	Spec     NormalizationSpec
}

// Validate checks that the selected source and runtime can form one normalization input.
func (plan AnalysisPlan) Validate(runID string) error {
	if plan.Spec.Validate() != nil || plan.Source.Validate() != nil ||
		plan.Source.RunID != runID || plan.Source.Kind != "raw" ||
		plan.Source.MediaType != "application/json" ||
		plan.Source.Format != plan.Spec.SourceFormat || plan.Producer.Validate() != nil {
		return run.ErrValidation
	}

	return nil
}

// AnalysisPlanResolver selects reviewed normalization configuration for exact raw evidence.
type AnalysisPlanResolver interface {
	// ResolveAnalysisPlan returns the single approved source, runtime and input
	// profile. It returns run.ErrForbidden when the Run context or evidence does
	// not match reviewed configuration and preserves context errors.
	ResolveAnalysisPlan(context.Context, string, run.Run, AnalysisInput) (AnalysisPlan, error)
}
