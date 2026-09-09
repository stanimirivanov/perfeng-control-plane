package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestLoadKubernetesExecutorTemplateConfigReturnsMountedFiles(t *testing.T) {
	root := t.TempDir()
	analysis := filepath.Join(root, "analysis.yaml")
	report := filepath.Join(root, "report.yaml")

	config, err := loadKubernetesExecutorTemplateConfig(mapLookup(map[string]string{
		analysisJobFileVariable: analysis,
		reportJobFileVariable:   report,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.AnalysisJobFile != analysis || config.ReportJobFile != report {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadKubernetesExecutorTemplateConfigRejectsInvalidPaths(t *testing.T) {
	root := t.TempDir()
	analysis := filepath.Join(root, "analysis.yaml")
	report := filepath.Join(root, "report.yaml")
	for name, mutate := range map[string]func(map[string]string){
		"missing analysis": func(values map[string]string) { delete(values, analysisJobFileVariable) },
		"relative analysis": func(values map[string]string) {
			values[analysisJobFileVariable] = "analysis.yaml"
		},
		"missing report": func(values map[string]string) { delete(values, reportJobFileVariable) },
		"relative report": func(values map[string]string) {
			values[reportJobFileVariable] = "report.yaml"
		},
		"same file": func(values map[string]string) {
			values[reportJobFileVariable] = values[analysisJobFileVariable]
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				analysisJobFileVariable: analysis,
				reportJobFileVariable:   report,
			}
			mutate(values)
			if _, err := loadKubernetesExecutorTemplateConfig(mapLookup(values)); !errors.Is(
				err, ErrInvalidConfiguration,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := loadKubernetesExecutorTemplateConfig(nil); !errors.Is(
		err, ErrInvalidConfiguration,
	) {
		t.Fatalf("nil environment error = %v", err)
	}
}

func TestLoadKubernetesExecutorConfigBuildsFixedStagingContract(t *testing.T) {
	files := writeExecutorJobs(t)

	config, err := LoadKubernetesExecutorConfig(files)
	if err != nil {
		t.Fatal(err)
	}
	if config.Analysis.ContainerName != analysisContainerName ||
		config.Analysis.InputContainerName != inputContainerName ||
		config.Analysis.ManifestPath != analysisManifestPath ||
		config.Analysis.SourcePath != analysisSourcePath ||
		config.Analysis.OutputPath != analysisOutputPath ||
		config.Report.ContainerName != reportContainerName ||
		config.Report.InputContainerName != inputContainerName ||
		config.Report.CandidatePath != reportCandidatePath ||
		config.Report.CandidateArtifactPath != reportCandidateArtifactPath ||
		config.Report.PolicyPath != reportPolicyPath ||
		config.Report.ReferenceDirectory != reportReferenceDirectory ||
		config.Report.OutputPath != reportOutputPath {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadKubernetesExecutorConfigRejectsUnsafeDocuments(t *testing.T) {
	valid := writeExecutorJobs(t)
	validJob, err := yaml.Marshal(executorJob(analysisContainerName))
	if err != nil {
		t.Fatal(err)
	}
	multipleDocuments := append(append([]byte(nil), validJob...), []byte("---\n")...)
	multipleDocuments = append(multipleDocuments, validJob...)
	for name, content := range map[string][]byte{
		"empty":              {},
		"oversized":          []byte(strings.Repeat(" ", maximumJobFileBytes+1)),
		"unknown field":      []byte("apiVersion: batch/v1\nkind: Job\nunknown: true\n"),
		"duplicate field":    []byte("apiVersion: batch/v1\nkind: Job\nkind: Job\n"),
		"multiple documents": multipleDocuments,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "analysis.yaml")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			files := valid
			files.AnalysisJobFile = path
			if _, err := LoadKubernetesExecutorConfig(files); !errors.Is(
				err, ErrInvalidConfiguration,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadKubernetesExecutorConfigRejectsInvalidTemplateAndRedactsPath(t *testing.T) {
	files := writeExecutorJobs(t)
	secret := "secret-template-location"
	path := filepath.Join(t.TempDir(), secret+".yaml")
	data, err := yaml.Marshal(executorJob("wrong-container"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	files.AnalysisJobFile = path

	_, err = LoadKubernetesExecutorConfig(files)
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}

func writeExecutorJobs(t *testing.T) KubernetesExecutorTemplateConfig {
	t.Helper()
	root := t.TempDir()
	analysis := filepath.Join(root, "analysis.yaml")
	report := filepath.Join(root, "report.yaml")
	for path, job := range map[string]any{
		analysis: executorJob(analysisContainerName),
		report:   executorJob(reportContainerName),
	} {
		data, err := yaml.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return KubernetesExecutorTemplateConfig{AnalysisJobFile: analysis, ReportJobFile: report}
}
