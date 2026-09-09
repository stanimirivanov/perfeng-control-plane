package app

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"

	batchv1 "k8s.io/api/batch/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
)

const (
	analysisJobFileVariable = "PERFENG_ANALYSIS_JOB_FILE"
	reportJobFileVariable   = "PERFENG_REPORT_JOB_FILE"
	maximumJobFileBytes     = 1 << 20
)

const (
	analysisContainerName       = "normalizer"
	inputContainerName          = "stage-inputs"
	analysisManifestPath        = "/workspace/input/raw-result.json"
	analysisSourcePath          = "/workspace/input/source.json"
	analysisOutputPath          = "/workspace/output/normalized-result.json"
	reportContainerName         = "reporter"
	reportCandidatePath         = "/workspace/input/candidate/normalized-result.json"
	reportCandidateArtifactPath = "/workspace/input/candidate/artifact-reference.json"
	reportPolicyPath            = "/workspace/input/performance-policy.json"
	reportReferenceDirectory    = "/workspace/input/references"
	reportOutputPath            = "/workspace/output/analysis-result.json"
)

// KubernetesExecutorTemplateConfig identifies the reviewed analysis and report
// Job documents mounted into the control-plane process.
type KubernetesExecutorTemplateConfig struct {
	AnalysisJobFile string
	ReportJobFile   string
}

// Validate requires distinct canonical absolute paths for both Job documents.
func (config KubernetesExecutorTemplateConfig) Validate() error {
	if !validCanonicalAbsolutePath(config.AnalysisJobFile) {
		return configurationError(analysisJobFileVariable)
	}
	if !validCanonicalAbsolutePath(config.ReportJobFile) ||
		config.ReportJobFile == config.AnalysisJobFile {
		return configurationError(reportJobFileVariable)
	}

	return nil
}

// LoadKubernetesExecutorTemplateConfig reads the two mounted Job paths.
func LoadKubernetesExecutorTemplateConfig() (KubernetesExecutorTemplateConfig, error) {
	return loadKubernetesExecutorTemplateConfig(os.LookupEnv)
}

func loadKubernetesExecutorTemplateConfig(
	lookup lookupEnvironment,
) (KubernetesExecutorTemplateConfig, error) {
	if lookup == nil {
		return KubernetesExecutorTemplateConfig{}, configurationError("environment")
	}
	analysis, err := requiredValue(lookup, analysisJobFileVariable)
	if err != nil {
		return KubernetesExecutorTemplateConfig{}, err
	}
	report, err := requiredValue(lookup, reportJobFileVariable)
	if err != nil {
		return KubernetesExecutorTemplateConfig{}, err
	}
	config := KubernetesExecutorTemplateConfig{AnalysisJobFile: analysis, ReportJobFile: report}
	if err := config.Validate(); err != nil {
		return KubernetesExecutorTemplateConfig{}, err
	}

	return config, nil
}

// LoadKubernetesExecutorConfig strictly loads and validates both reviewed Jobs
// together with the platform-owned staging layout.
func LoadKubernetesExecutorConfig(
	templates KubernetesExecutorTemplateConfig,
) (KubernetesExecutorConfig, error) {
	if templates.Validate() != nil {
		return KubernetesExecutorConfig{}, ErrInvalidConfiguration
	}
	analysis, err := loadJob(templates.AnalysisJobFile)
	if err != nil {
		return KubernetesExecutorConfig{}, ErrInvalidConfiguration
	}
	report, err := loadJob(templates.ReportJobFile)
	if err != nil {
		return KubernetesExecutorConfig{}, ErrInvalidConfiguration
	}
	config := KubernetesExecutorConfig{
		Analysis: reconcile.AnalysisJobConfig{
			Template: analysis, ContainerName: analysisContainerName,
			InputContainerName: inputContainerName,
			ManifestPath:       analysisManifestPath,
			SourcePath:         analysisSourcePath,
			OutputPath:         analysisOutputPath,
		},
		Report: reconcile.ReportJobConfig{
			Template: report, ContainerName: reportContainerName,
			InputContainerName:    inputContainerName,
			CandidatePath:         reportCandidatePath,
			CandidateArtifactPath: reportCandidateArtifactPath,
			PolicyPath:            reportPolicyPath,
			ReferenceDirectory:    reportReferenceDirectory,
			OutputPath:            reportOutputPath,
		},
	}
	if config.Validate() != nil {
		return KubernetesExecutorConfig{}, ErrInvalidConfiguration
	}

	return config, nil
}

func loadJob(filePath string) (*batchv1.Job, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumJobFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) == 0 || len(data) > maximumJobFileBytes {
		return nil, ErrInvalidConfiguration
	}
	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	document, err := reader.Read()
	if err != nil || len(bytes.TrimSpace(document)) == 0 {
		return nil, ErrInvalidConfiguration
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidConfiguration
	}
	var job batchv1.Job
	if yaml.UnmarshalStrict(document, &job) != nil {
		return nil, ErrInvalidConfiguration
	}

	return &job, nil
}
