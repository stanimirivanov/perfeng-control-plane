package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type resolveReportPlanFunc func(
	context.Context,
	string,
	run.Run,
	ReportingInput,
) (ReportPlan, error)

func (resolve resolveReportPlanFunc) ResolveReportPlan(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (ReportPlan, error) {
	return resolve(ctx, principal, current, input)
}

func reportJobConfig() ReportJobConfig {
	template := reusableAnalysisJob()
	template.Spec.Template.Spec.Containers[0].Name = "reporter"

	return ReportJobConfig{
		Template: template, ContainerName: "reporter", InputContainerName: "stage-inputs",
		CandidatePath:         "/workspace/input/candidate/normalized-result.json",
		CandidateArtifactPath: "/workspace/input/candidate/artifact-reference.json",
		PolicyPath:            "/workspace/input/performance-policy.json",
		ReferenceDirectory:    "/workspace/input/references",
		OutputPath:            "/workspace/output/analysis-result.json",
	}
}

func reportJobFixture() (run.Run, ReportingInput, ReportPlan) {
	current := analysisRun()
	current.State = run.StateReporting
	input := ReportingInput{Candidate: normalizedArtifact(current.ID)}
	policyBytes := []byte(`{
		"apiVersion":"performance.perfeng.io/v1",
		"kind":"PerformancePolicy",
		"metadata":{"name":"checkout-policy","version":"1.0.0","owner":"checkout-team"},
		"spec":{"mode":"inform","missingData":"inconclusive","rules":[
			{"id":"latency","metric":{"name":"api.latency","statistic":"p95","unit":"ms"},"regression":{"direction":"lower-is-better","practicalDifference":{"kind":"relative","value":0.1},"reference":{"baselineId":"release","version":"1.0.0"}}},
			{"id":"errors","metric":{"name":"api.errors","statistic":"mean","unit":"ratio"},"regression":{"direction":"lower-is-better","practicalDifference":{"kind":"absolute","value":0.01},"reference":{"baselineId":"stable","version":"2.0.0"}}},
			{"id":"throughput","metric":{"name":"api.rate","statistic":"mean","unit":"req/s"},"regression":{"direction":"higher-is-better","practicalDifference":{"kind":"relative","value":0.05},"reference":{"baselineId":"missing","version":"3.0.0"}}}
		]}}
	`)
	digest := sha256.Sum256(policyBytes)
	current.Request.Policy = run.Reference{
		ID: "checkout-policy", Version: "1.0.0", SHA256: hex.EncodeToString(digest[:]),
	}
	reference := normalizedArtifact("perf-20260901-120000-87654321")
	reference.ID = "20000000-0000-4000-8000-000000000005"
	reference.URI = "s3://perfeng-artifacts/runs/" + reference.RunID + "/normalized-result.json"
	reference.SHA256 = strings.Repeat("e", 64)
	plan := ReportPlan{
		Candidate: input.Candidate, PolicyBytes: policyBytes, PolicyMode: "inform",
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "0.7.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("a", 64),
		},
		Baselines: []policy.BaselineResolution{
			{ID: "release", Version: "1.0.0", Artifact: &reference},
			{ID: "stable", Version: "2.0.0", Artifact: &reference},
			{ID: "missing", Version: "3.0.0"},
		},
	}

	return current, input, plan
}

func TestKubernetesReportJobResolverBuildsPolicyKeyedArguments(t *testing.T) {
	current, input, plan := reportJobFixture()
	plans := resolveReportPlanFunc(func(
		ctx context.Context,
		principal string,
		actualRun run.Run,
		actualInput ReportingInput,
	) (ReportPlan, error) {
		if ctx.Err() != nil || principal != "principal-a" || actualRun != current ||
			actualInput != input {
			t.Fatal("plan resolver input changed")
		}

		return plan, nil
	})
	config := reportJobConfig()
	original := config.Template.DeepCopy()
	resolver, err := NewKubernetesReportJobResolver(plans, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Template.Spec.Template.Spec.Containers[0].Name = "changed"

	template, err := resolver.ResolveReportJob(
		context.Background(), "principal-a", current, input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := kubernetes.ValidateReusableJobTemplate(template); err != nil {
		t.Fatal(err)
	}
	container := jobContainer(template, "reporter")
	if container == nil || container.Image != plan.Producer.Image ||
		!reflect.DeepEqual(container.Command, []string{reportExecutable}) {
		t.Fatalf("report container = %+v", container)
	}
	referenceDirectory := "/workspace/input/references/20000000-0000-4000-8000-000000000005"
	want := []string{
		"--candidate", "/workspace/input/candidate/normalized-result.json",
		"--candidate-artifact", "/workspace/input/candidate/artifact-reference.json",
		"--policy", "/workspace/input/performance-policy.json",
		"--baseline", "release", "1.0.0",
		referenceDirectory + "/normalized-result.json",
		referenceDirectory + "/artifact-reference.json",
		"--baseline", "stable", "2.0.0",
		referenceDirectory + "/normalized-result.json",
		referenceDirectory + "/artifact-reference.json",
		"--producer-image", plan.Producer.Image,
		"--output", "/workspace/output/analysis-result.json",
	}
	if !reflect.DeepEqual(container.Args, want) {
		t.Fatalf("arguments = %q, want %q", container.Args, want)
	}
	inputContainer := jobInitContainer(template, "stage-inputs")
	wantEnvironment := []corev1.EnvVar{
		{Name: "AWS_REGION", Value: "eu-west-1"},
		{Name: reportCandidateArtifactIDEnvironment, Value: plan.Candidate.ID},
		{Name: reportCandidateRunIDEnvironment, Value: plan.Candidate.RunID},
		{Name: reportCandidateKindEnvironment, Value: plan.Candidate.Kind},
		{Name: reportCandidateURIEnvironment, Value: plan.Candidate.URI},
		{Name: reportCandidateSHA256Environment, Value: plan.Candidate.SHA256},
		{
			Name:  reportCandidateSizeEnvironment,
			Value: strconv.FormatInt(plan.Candidate.SizeBytes, 10),
		},
		{Name: reportCandidateMediaTypeEnvironment, Value: plan.Candidate.MediaType},
		{Name: reportCandidateFormatEnvironment, Value: plan.Candidate.Format},
		{Name: reportCandidatePathEnvironment, Value: config.CandidatePath},
		{Name: reportCandidateReferenceEnvironment, Value: config.CandidateArtifactPath},
		{Name: reportPolicyIDEnvironment, Value: current.Request.Policy.ID},
		{Name: reportPolicyVersionEnvironment, Value: current.Request.Policy.Version},
		{Name: reportPolicySHA256Environment, Value: current.Request.Policy.SHA256},
		{Name: reportPolicyPathEnvironment, Value: config.PolicyPath},
	}
	if inputContainer == nil || !reflect.DeepEqual(inputContainer.Env, wantEnvironment) {
		t.Fatalf("input environment = %+v, want %+v", inputContainer, wantEnvironment)
	}
	if original.Spec.Template.Spec.Containers[0].Args != nil ||
		original.Spec.Template.Spec.Containers[0].Command != nil ||
		!reflect.DeepEqual(
			original.Spec.Template.Spec.InitContainers[0].Env,
			[]corev1.EnvVar{{Name: "AWS_REGION", Value: "eu-west-1"}},
		) {
		t.Fatal("constructor changed its input template")
	}

	container.Args[0] = "changed"
	inputContainer.Env[1].Value = "changed"
	again, err := resolver.ResolveReportJob(
		context.Background(), "principal-a", current, input,
	)
	if err != nil || jobContainer(again, "reporter").Args[0] != "--candidate" ||
		jobInitContainer(again, "stage-inputs").Env[1].Value != plan.Candidate.ID {
		t.Fatal("resolved Jobs share mutable state")
	}
}

func TestKubernetesReportJobResolverRejectsInvalidConfiguration(t *testing.T) {
	validPlans := resolveReportPlanFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (ReportPlan, error) {
		return ReportPlan{}, nil
	})
	for _, test := range []struct {
		name   string
		plans  ReportPlanResolver
		mutate func(*ReportJobConfig)
	}{
		{name: "plan resolver", mutate: func(*ReportJobConfig) {}},
		{name: "template", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.Template = nil
		}},
		{name: "container", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.ContainerName = "missing"
		}},
		{name: "input container", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.InputContainerName = "missing"
		}},
		{name: "duplicate input container", plans: validPlans, mutate: func(config *ReportJobConfig) {
			container := config.Template.Spec.Template.Spec.InitContainers[0]
			config.Template.Spec.Template.Spec.InitContainers = append(
				config.Template.Spec.Template.Spec.InitContainers,
				container,
			)
		}},
		{name: "reserved candidate environment", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.Template.Spec.Template.Spec.InitContainers[0].Env = append(
				config.Template.Spec.Template.Spec.InitContainers[0].Env,
				corev1.EnvVar{
					Name:  reportCandidateURIEnvironment,
					Value: "s3://untrusted/candidate.json",
				},
			)
		}},
		{name: "reserved policy environment", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.Template.Spec.Template.Spec.InitContainers[0].Env = append(
				config.Template.Spec.Template.Spec.InitContainers[0].Env,
				corev1.EnvVar{Name: reportPolicySHA256Environment, Value: strings.Repeat("0", 64)},
			)
		}},
		{name: "predefined command", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.Template.Spec.Template.Spec.Containers[0].Command = []string{"other"}
		}},
		{name: "predefined arguments", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.Template.Spec.Template.Spec.Containers[0].Args = []string{"run"}
		}},
		{name: "relative path", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.PolicyPath = "policy.json"
		}},
		{name: "unclean path", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.ReferenceDirectory = "/workspace/../references"
		}},
		{name: "duplicate path", plans: validPlans, mutate: func(config *ReportJobConfig) {
			config.OutputPath = config.PolicyPath
		}},
		{name: "invalid Job", plans: validPlans, mutate: func(config *ReportJobConfig) {
			*config.Template.Spec.BackoffLimit = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := reportJobConfig()
			test.mutate(&config)
			if _, err := NewKubernetesReportJobResolver(test.plans, config); !errors.Is(
				err, run.ErrValidation,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestKubernetesReportJobResolverValidatesResolvedPlanAndContext(t *testing.T) {
	current, input, valid := reportJobFixture()
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     ReportingInput
		plan      ReportPlan
		planErr   error
		want      error
	}{
		{name: "principal", state: run.StateReporting, input: input, plan: valid, want: run.ErrValidation},
		{name: "state", principal: "principal-a", state: run.StateRunning, input: input, plan: valid, want: run.ErrValidation},
		{name: "input", principal: "principal-a", state: run.StateReporting, plan: valid, want: run.ErrValidation},
		{name: "resolver", principal: "principal-a", state: run.StateReporting, input: input, planErr: run.ErrForbidden, want: run.ErrForbidden},
		{name: "invalid plan", principal: "principal-a", state: run.StateReporting, input: input, want: run.ErrValidation},
		{name: "foreign candidate", principal: "principal-a", state: run.StateReporting, input: input, plan: func() ReportPlan {
			plan := valid.Clone()
			plan.Candidate.ID = "99999999-9999-4999-8999-999999999999"
			plan.Candidate.URI += ".other"
			return plan
		}(), want: run.ErrValidation},
		{name: "changed policy", principal: "principal-a", state: run.StateReporting, input: input, plan: func() ReportPlan {
			plan := valid.Clone()
			plan.PolicyBytes = append(plan.PolicyBytes, ' ')
			return plan
		}(), want: run.ErrValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			plans := resolveReportPlanFunc(func(
				context.Context, string, run.Run, ReportingInput,
			) (ReportPlan, error) {
				return test.plan, test.planErr
			})
			resolver, err := NewKubernetesReportJobResolver(plans, reportJobConfig())
			if err != nil {
				t.Fatal(err)
			}
			candidate := current
			candidate.State = test.state
			if _, err = resolver.ResolveReportJob(
				context.Background(), test.principal, candidate, test.input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	var nilResolver *KubernetesReportJobResolver
	if _, err := nilResolver.ResolveReportJob(
		context.Background(), "principal-a", current, input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver error = %v", err)
	}

	resolver, err := NewKubernetesReportJobResolver(
		resolveReportPlanFunc(func(
			context.Context, string, run.Run, ReportingInput,
		) (ReportPlan, error) {
			t.Fatal("cancelled resolution reached the plan resolver")
			return ReportPlan{}, nil
		}),
		reportJobConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = resolver.ResolveReportJob(ctx, "principal-a", current, input); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("cancellation error = %v", err)
	}
}
