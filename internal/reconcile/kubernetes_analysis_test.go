package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type resolveAnalysisJobFunc func(context.Context, string, run.Run, AnalysisInput) (*batchv1.Job, error)

func (resolve resolveAnalysisJobFunc) ResolveAnalysisJob(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (*batchv1.Job, error) {
	return resolve(ctx, principal, current, input)
}

type ensureAnalysisJobFunc func(context.Context, string, *batchv1.Job) (kubernetes.Observation, error)

func (ensure ensureAnalysisJobFunc) EnsureAnalysisJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (kubernetes.Observation, error) {
	return ensure(ctx, runID, template)
}

type collectNormalizedFunc func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error)

func (collect collectNormalizedFunc) CollectNormalizedArtifact(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (run.Artifact, error) {
	return collect(ctx, principal, current, input)
}

func kubernetesAnalysisFixture(t *testing.T) (*KubernetesAnalysisExecutor, run.Run, AnalysisInput, run.Artifact) {
	t.Helper()
	claim := boundClaim(run.StateAnalyzing)
	artifacts := analysisArtifacts(claim.Run.ID)
	input := AnalysisInput{Manifest: artifacts[2], Sources: artifacts[:2]}
	output := normalizedArtifact(claim.Run.ID)
	template := &batchv1.Job{}

	resolver := resolveAnalysisJobFunc(func(
		_ context.Context,
		principal string,
		current run.Run,
		actual AnalysisInput,
	) (*batchv1.Job, error) {
		if principal != claim.Lease.Principal || current != claim.Run || !reflect.DeepEqual(actual, input) {
			t.Fatal("resolver input changed")
		}
		return template, nil
	})
	controller := ensureAnalysisJobFunc(func(
		_ context.Context,
		runID string,
		actual *batchv1.Job,
	) (kubernetes.Observation, error) {
		if runID != claim.Run.ID || actual != template {
			t.Fatal("controller input changed")
		}
		return kubernetes.Observation{Phase: kubernetes.JobSucceeded}, nil
	})
	collector := collectNormalizedFunc(func(
		_ context.Context,
		principal string,
		current run.Run,
		actual AnalysisInput,
	) (run.Artifact, error) {
		if principal != claim.Lease.Principal || current != claim.Run || !reflect.DeepEqual(actual, input) {
			t.Fatal("collector input changed")
		}
		return output, nil
	})
	executor, err := NewKubernetesAnalysisExecutor(resolver, controller, collector)
	if err != nil {
		t.Fatal(err)
	}

	return executor, claim.Run, input, output
}

func TestKubernetesAnalysisExecutorCollectsOnlyAfterSuccess(t *testing.T) {
	executor, current, input, want := kubernetesAnalysisFixture(t)
	artifact, err := executor.Normalize(context.Background(), "principal-a", current, input)
	if err != nil || artifact != want {
		t.Fatalf("Normalize() = %+v, %v", artifact, err)
	}
}

func TestKubernetesAnalysisExecutorMapsJobPhases(t *testing.T) {
	for _, test := range []struct {
		phase kubernetes.JobPhase
		want  error
	}{
		{kubernetes.JobPending, ErrAnalysisPending},
		{kubernetes.JobRunning, ErrAnalysisPending},
		{kubernetes.JobFailed, ErrAnalysisFailed},
		{kubernetes.JobAbsent, run.ErrValidation},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			executor, current, input, _ := kubernetesAnalysisFixture(t)
			collected := false
			executor.controller = ensureAnalysisJobFunc(func(context.Context, string, *batchv1.Job) (kubernetes.Observation, error) {
				return kubernetes.Observation{Phase: test.phase}, nil
			})
			executor.collector = collectNormalizedFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
				collected = true
				return run.Artifact{}, nil
			})
			if _, err := executor.Normalize(context.Background(), "principal-a", current, input); !errors.Is(err, test.want) {
				t.Fatalf("phase %s error = %v, want %v", test.phase, err, test.want)
			}
			if collected {
				t.Fatal("non-successful Job triggered output collection")
			}
		})
	}
}

func TestKubernetesAnalysisExecutorPreservesBoundaryErrors(t *testing.T) {
	for _, boundary := range []string{"resolver", "controller", "collector"} {
		t.Run(boundary, func(t *testing.T) {
			executor, current, input, _ := kubernetesAnalysisFixture(t)
			want := run.ErrUnavailable
			switch boundary {
			case "resolver":
				executor.resolver = resolveAnalysisJobFunc(func(context.Context, string, run.Run, AnalysisInput) (*batchv1.Job, error) {
					return nil, want
				})
			case "controller":
				executor.controller = ensureAnalysisJobFunc(func(context.Context, string, *batchv1.Job) (kubernetes.Observation, error) {
					return kubernetes.Observation{}, want
				})
			case "collector":
				executor.collector = collectNormalizedFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
					return run.Artifact{}, want
				})
			}
			if _, err := executor.Normalize(context.Background(), "principal-a", current, input); !errors.Is(err, want) {
				t.Fatalf("%s error = %v", boundary, err)
			}
		})
	}
}

func TestKubernetesAnalysisExecutorValidatesDependenciesAndTemplate(t *testing.T) {
	executor, current, input, _ := kubernetesAnalysisFixture(t)
	executor.resolver = resolveAnalysisJobFunc(func(context.Context, string, run.Run, AnalysisInput) (*batchv1.Job, error) {
		return nil, nil
	})
	if _, err := executor.Normalize(context.Background(), "principal-a", current, input); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil template error = %v", err)
	}

	valid, _, _, _ := kubernetesAnalysisFixture(t)
	for name, dependencies := range map[string][]any{
		"resolver":   {nil, valid.controller, valid.collector},
		"controller": {valid.resolver, nil, valid.collector},
		"collector":  {valid.resolver, valid.controller, nil},
	} {
		t.Run(name, func(t *testing.T) {
			resolver, _ := dependencies[0].(AnalysisJobResolver)
			controller, _ := dependencies[1].(AnalysisJobController)
			collector, _ := dependencies[2].(NormalizedArtifactCollector)
			if _, err := NewKubernetesAnalysisExecutor(resolver, controller, collector); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("missing dependency error = %v", err)
			}
		})
	}
}
