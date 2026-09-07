package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type inertExecutionTrust struct {
	inertArtifactTrust
}

func (inertExecutionTrust) ResolveAnalysisPlan(
	context.Context,
	string,
	run.Run,
	reconcile.AnalysisInput,
) (reconcile.AnalysisPlan, error) {
	panic("execution trust called during construction")
}

func executorJob(containerName string) *batchv1.Job {
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false

	return &batchv1.Job{Spec: batchv1.JobSpec{
		BackoffLimit: &zero, Completions: &one, Parallelism: &one,
		ActiveDeadlineSeconds: &deadline,
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
			Containers: []corev1.Container{{
				Name:  containerName,
				Image: "ghcr.io/example/placeholder@sha256:" + strings.Repeat("0", 64),
			}},
		}},
	}}
}

func kubernetesExecutorConfig() KubernetesExecutorConfig {
	return KubernetesExecutorConfig{
		Analysis: reconcile.AnalysisJobConfig{
			Template: executorJob("normalizer"), ContainerName: "normalizer",
			ManifestPath: "/workspace/input/raw-result.json",
			SourcePath:   "/workspace/input/source.json",
			OutputPath:   "/workspace/output/normalized-result.json",
		},
		Report: reconcile.ReportJobConfig{
			Template: executorJob("reporter"), ContainerName: "reporter",
			CandidatePath:         "/workspace/input/candidate/normalized-result.json",
			CandidateArtifactPath: "/workspace/input/candidate/artifact-reference.json",
			PolicyPath:            "/workspace/input/performance-policy.json",
			ReferenceDirectory:    "/workspace/input/references",
			OutputPath:            "/workspace/output/analysis-result.json",
		},
	}
}

func executorDependencies(t *testing.T) (
	ExecutionTrust,
	baseline.Repository,
	KubernetesControllers,
	ArtifactCollectors,
) {
	t.Helper()
	trust := inertExecutionTrust{}
	baselines := inertBaselineRepository{}
	clients, _ := namespacedKubernetesClients()
	controllers, err := NewKubernetesControllers(clients)
	if err != nil {
		t.Fatal(err)
	}
	collectors, err := NewArtifactCollectors(
		inertArtifactStorage{},
		trust,
		baselines,
		ArtifactVerificationConfig{ContractsVersion: "0.12.0"},
	)
	if err != nil {
		t.Fatal(err)
	}

	return trust, baselines, controllers, collectors
}

func TestNewKubernetesExecutorsComposesStagesWithoutRequests(t *testing.T) {
	trust, baselines, controllers, collectors := executorDependencies(t)
	clients, kubernetesClient := namespacedKubernetesClients()
	controllers, err := NewKubernetesControllers(clients)
	if err != nil {
		t.Fatal(err)
	}

	executors, err := NewKubernetesExecutors(
		trust,
		baselines,
		controllers,
		collectors,
		kubernetesExecutorConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if executors.Analysis == nil || executors.Reporting == nil {
		t.Fatalf("executors = %#v", executors)
	}
	if actions := kubernetesClient.Actions(); len(actions) != 0 {
		t.Fatalf("construction made Kubernetes requests: %#v", actions)
	}
}

func TestNewKubernetesExecutorsRejectsInvalidDependencies(t *testing.T) {
	trust, baselines, controllers, collectors := executorDependencies(t)
	validConfig := kubernetesExecutorConfig()
	tests := map[string]func() (KubernetesExecutors, error){
		"trust": func() (KubernetesExecutors, error) {
			return NewKubernetesExecutors(nil, baselines, controllers, collectors, validConfig)
		},
		"baselines": func() (KubernetesExecutors, error) {
			return NewKubernetesExecutors(trust, nil, controllers, collectors, validConfig)
		},
		"analysis controller": func() (KubernetesExecutors, error) {
			invalid := controllers
			invalid.Analysis = nil
			return NewKubernetesExecutors(trust, baselines, invalid, collectors, validConfig)
		},
		"reporting controller": func() (KubernetesExecutors, error) {
			invalid := controllers
			invalid.Reporting = nil
			return NewKubernetesExecutors(trust, baselines, invalid, collectors, validConfig)
		},
		"normalized collector": func() (KubernetesExecutors, error) {
			invalid := collectors
			invalid.Normalized = nil
			return NewKubernetesExecutors(trust, baselines, controllers, invalid, validConfig)
		},
		"report collector": func() (KubernetesExecutors, error) {
			invalid := collectors
			invalid.Report = nil
			return NewKubernetesExecutors(trust, baselines, controllers, invalid, validConfig)
		},
		"analysis config": func() (KubernetesExecutors, error) {
			invalid := kubernetesExecutorConfig()
			invalid.Analysis.Template = nil
			return NewKubernetesExecutors(trust, baselines, controllers, collectors, invalid)
		},
		"report config": func() (KubernetesExecutors, error) {
			invalid := kubernetesExecutorConfig()
			invalid.Report.PolicyPath = "policy.json"
			return NewKubernetesExecutors(trust, baselines, controllers, collectors, invalid)
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			executors, err := build()
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("executors = %#v, error = %v", executors, err)
			}
		})
	}
}

var _ ExecutionTrust = inertExecutionTrust{}
