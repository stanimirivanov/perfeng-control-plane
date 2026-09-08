package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type resolveAnalysisPlanFunc func(
	context.Context,
	string,
	run.Run,
	AnalysisInput,
) (AnalysisPlan, error)

func (resolve resolveAnalysisPlanFunc) ResolveAnalysisPlan(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (AnalysisPlan, error) {
	return resolve(ctx, principal, current, input)
}

func reusableAnalysisJob() *batchv1.Job {
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false

	return &batchv1.Job{Spec: batchv1.JobSpec{
		BackoffLimit: &zero, Completions: &one, Parallelism: &one,
		ActiveDeadlineSeconds: &deadline,
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
			Containers: []corev1.Container{{
				Name:  "normalizer",
				Image: "ghcr.io/example/placeholder@sha256:" + strings.Repeat("0", 64),
			}},
		}},
	}}
}

func analysisJobConfig() AnalysisJobConfig {
	template := reusableAnalysisJob()

	return AnalysisJobConfig{
		Template: template, ContainerName: "normalizer",
		ManifestPath: "/workspace/input/raw-result.json",
		SourcePath:   "/workspace/input/source.json",
		OutputPath:   "/workspace/output/normalized-result.json",
	}
}

func analysisPlan(source run.Artifact, spec NormalizationSpec) AnalysisPlan {
	return AnalysisPlan{
		Source: source,
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "0.8.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("a", 64),
		},
		Spec: spec,
	}
}

func analysisRun() run.Run {
	current := boundClaim(run.StateAnalyzing).Run
	current.Request = run.Request{
		TestSuite: "checkout-api",
		Catalogue: run.Reference{
			ID: "application-tests", Version: "1.0.0", SHA256: strings.Repeat("1", 64),
		},
		Profile: "smoke",
		Candidate: run.Candidate{
			GitSHA: strings.Repeat("2", 40),
			Image:  "ghcr.io/example/sample-api@sha256:" + strings.Repeat("3", 64),
		},
		Environment: run.Reference{
			ID: "local-kind", Version: "1.0.0", SHA256: strings.Repeat("4", 64),
		},
		Policy: run.Reference{
			ID: "checkout-policy", Version: "1.0.0", SHA256: strings.Repeat("5", 64),
		},
	}

	return current
}

func TestKubernetesAnalysisJobResolverBuildsToolSpecificArguments(t *testing.T) {
	for _, test := range []struct {
		name     string
		spec     NormalizationSpec
		wantTail []string
	}{
		{
			name: "k6", spec: NormalizationSpec{
				SourceFormat: K6SummaryFormat, Layout: K6LegacyExportLayout,
			},
			wantTail: []string{
				"--layout", K6LegacyExportLayout, "--summary-covers-window",
				"--output", "/workspace/output/normalized-result.json",
			},
		},
		{
			name: "Playwright", spec: NormalizationSpec{
				SourceFormat: PlaywrightMeasurementsV2Format,
			},
			wantTail: []string{"--output", "/workspace/output/normalized-result.json"},
		},
		{
			name: "kube-burner", spec: NormalizationSpec{
				SourceFormat: KubeBurnerPodLatencyFormat,
			},
			wantTail: []string{"--output", "/workspace/output/normalized-result.json"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := analysisRun()
			artifacts := analysisArtifacts(current.ID)
			input := AnalysisInput{Manifest: artifacts[2], Sources: artifacts[:2]}
			input.Sources[0].Format = test.spec.SourceFormat
			plan := analysisPlan(input.Sources[0], test.spec)
			plans := resolveAnalysisPlanFunc(func(
				_ context.Context,
				principal string,
				actualRun run.Run,
				actualInput AnalysisInput,
			) (AnalysisPlan, error) {
				if principal != "principal-a" || actualRun != current ||
					!reflect.DeepEqual(actualInput, input) {
					t.Fatal("plan resolver input changed")
				}
				return plan, nil
			})
			config := analysisJobConfig()
			original := config.Template.DeepCopy()
			resolver, err := NewKubernetesAnalysisJobResolver(plans, config)
			if err != nil {
				t.Fatal(err)
			}
			config.Template.Spec.Template.Spec.Containers[0].Name = "changed"

			template, err := resolver.ResolveAnalysisJob(
				context.Background(), "principal-a", current, input,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := kubernetes.ValidateReusableJobTemplate(template); err != nil {
				t.Fatal(err)
			}
			container := jobContainer(template, "normalizer")
			if container == nil || container.Image != plan.Producer.Image {
				t.Fatalf("normalizer container = %+v", container)
			}
			wantPrefix := []string{
				"--manifest", "/workspace/input/raw-result.json",
				"--summary", "/workspace/input/source.json",
				"--artifact-id", plan.Source.ID,
				"--producer-image", plan.Producer.Image,
			}
			want := append(wantPrefix, test.wantTail...)
			if !reflect.DeepEqual(container.Args, want) {
				t.Fatalf("arguments = %q, want %q", container.Args, want)
			}
			if original.Spec.Template.Spec.Containers[0].Args != nil {
				t.Fatal("constructor changed its input template")
			}

			container.Args[0] = "changed"
			again, err := resolver.ResolveAnalysisJob(
				context.Background(), "principal-a", current, input,
			)
			if err != nil || jobContainer(again, "normalizer").Args[0] != "--manifest" {
				t.Fatal("resolved Jobs share mutable state")
			}
		})
	}
}

func TestKubernetesAnalysisJobResolverRejectsInvalidConfiguration(t *testing.T) {
	validPlanResolver := resolveAnalysisPlanFunc(func(
		context.Context, string, run.Run, AnalysisInput,
	) (AnalysisPlan, error) {
		return AnalysisPlan{}, nil
	})
	for _, test := range []struct {
		name   string
		plans  AnalysisPlanResolver
		mutate func(*AnalysisJobConfig)
	}{
		{name: "plan resolver", mutate: func(*AnalysisJobConfig) {}},
		{name: "template", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.Template = nil
		}},
		{name: "container", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.ContainerName = "missing"
		}},
		{name: "predefined command", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.Template.Spec.Template.Spec.Containers[0].Command = []string{"sh"}
		}},
		{name: "predefined arguments", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.Template.Spec.Template.Spec.Containers[0].Args = []string{"run"}
		}},
		{name: "relative path", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.SourcePath = "input.json"
		}},
		{name: "unclean path", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.SourcePath = "/workspace/../input.json"
		}},
		{name: "duplicate path", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			config.SourcePath = config.ManifestPath
		}},
		{name: "invalid Job", plans: validPlanResolver, mutate: func(config *AnalysisJobConfig) {
			*config.Template.Spec.BackoffLimit = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := analysisJobConfig()
			test.mutate(&config)
			if _, err := NewKubernetesAnalysisJobResolver(test.plans, config); !errors.Is(
				err, run.ErrValidation,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestKubernetesAnalysisJobResolverValidatesResolvedPlanAndContext(t *testing.T) {
	current := analysisRun()
	artifacts := analysisArtifacts(current.ID)
	input := AnalysisInput{Manifest: artifacts[2], Sources: artifacts[:2]}
	valid := analysisPlan(
		input.Sources[0],
		NormalizationSpec{SourceFormat: K6SummaryFormat, Layout: K6TypedLayout},
	)

	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     AnalysisInput
		plan      AnalysisPlan
		planErr   error
		want      error
	}{
		{name: "principal", state: run.StateAnalyzing, input: input, plan: valid, want: run.ErrValidation},
		{name: "state", principal: "principal-a", state: run.StateRunning, input: input, plan: valid, want: run.ErrValidation},
		{name: "input", principal: "principal-a", state: run.StateAnalyzing, plan: valid, want: run.ErrValidation},
		{name: "resolver", principal: "principal-a", state: run.StateAnalyzing, input: input, planErr: run.ErrForbidden, want: run.ErrForbidden},
		{name: "invalid plan", principal: "principal-a", state: run.StateAnalyzing, input: input, want: run.ErrValidation},
		{name: "foreign source", principal: "principal-a", state: run.StateAnalyzing, input: input, plan: func() AnalysisPlan {
			plan := valid
			plan.Source.ID = "99999999-9999-4999-8999-999999999999"
			plan.Source.URI += ".other"
			return plan
		}(), want: run.ErrValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			plans := resolveAnalysisPlanFunc(func(
				context.Context, string, run.Run, AnalysisInput,
			) (AnalysisPlan, error) {
				return test.plan, test.planErr
			})
			resolver, err := NewKubernetesAnalysisJobResolver(plans, analysisJobConfig())
			if err != nil {
				t.Fatal(err)
			}
			candidate := current
			candidate.State = test.state
			if _, err = resolver.ResolveAnalysisJob(
				context.Background(), test.principal, candidate, test.input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	var nilResolver *KubernetesAnalysisJobResolver
	if _, err := nilResolver.ResolveAnalysisJob(
		context.Background(), "principal-a", current, input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver error = %v", err)
	}

	resolver, err := NewKubernetesAnalysisJobResolver(
		resolveAnalysisPlanFunc(func(
			context.Context, string, run.Run, AnalysisInput,
		) (AnalysisPlan, error) {
			t.Fatal("cancelled resolution reached the plan resolver")
			return AnalysisPlan{}, nil
		}),
		analysisJobConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = resolver.ResolveAnalysisJob(ctx, "principal-a", current, input); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("cancellation error = %v", err)
	}
}
