package reconcile

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type resolveReportJobFunc func(context.Context, string, run.Run, ReportingInput) (*batchv1.Job, error)

func (resolve resolveReportJobFunc) ResolveReportJob(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (*batchv1.Job, error) {
	return resolve(ctx, principal, current, input)
}

type ensureReportJobFunc func(context.Context, string, *batchv1.Job) (kubernetes.Observation, error)

func (ensure ensureReportJobFunc) EnsureReportJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (kubernetes.Observation, error) {
	return ensure(ctx, runID, template)
}

type collectReportFunc func(context.Context, string, run.Run, ReportingInput) (run.Artifact, error)

func (collect collectReportFunc) CollectReportArtifact(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (run.Artifact, error) {
	return collect(ctx, principal, current, input)
}

func kubernetesReportingFixture(
	t *testing.T,
) (*KubernetesReportExecutor, run.Run, ReportingInput, run.Artifact) {
	t.Helper()
	claim := boundClaim(run.StateReporting)
	input := ReportingInput{Candidate: normalizedArtifact(claim.Run.ID)}
	output := reportArtifact(claim.Run.ID)
	template := &batchv1.Job{}

	resolver := resolveReportJobFunc(func(
		_ context.Context,
		principal string,
		current run.Run,
		actual ReportingInput,
	) (*batchv1.Job, error) {
		if principal != claim.Lease.Principal || current != claim.Run || actual != input {
			t.Fatal("resolver input changed")
		}
		return template, nil
	})
	controller := ensureReportJobFunc(func(
		_ context.Context,
		runID string,
		actual *batchv1.Job,
	) (kubernetes.Observation, error) {
		if runID != claim.Run.ID || actual != template {
			t.Fatal("controller input changed")
		}
		return kubernetes.Observation{Phase: kubernetes.JobSucceeded}, nil
	})
	collector := collectReportFunc(func(
		_ context.Context,
		principal string,
		current run.Run,
		actual ReportingInput,
	) (run.Artifact, error) {
		if principal != claim.Lease.Principal || current != claim.Run || actual != input {
			t.Fatal("collector input changed")
		}
		return output, nil
	})
	executor, err := NewKubernetesReportExecutor(resolver, controller, collector)
	if err != nil {
		t.Fatal(err)
	}

	return executor, claim.Run, input, output
}

func TestKubernetesReportExecutorCollectsOnlyAfterSuccess(t *testing.T) {
	executor, current, input, want := kubernetesReportingFixture(t)
	artifact, err := executor.Report(context.Background(), "principal-a", current, input)
	if err != nil || artifact != want {
		t.Fatalf("Report() = %+v, %v", artifact, err)
	}
}

func TestKubernetesReportExecutorMapsJobPhases(t *testing.T) {
	for _, test := range []struct {
		phase kubernetes.JobPhase
		want  error
	}{
		{kubernetes.JobPending, ErrReportPending},
		{kubernetes.JobRunning, ErrReportPending},
		{kubernetes.JobFailed, ErrReportFailed},
		{kubernetes.JobAbsent, run.ErrValidation},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			executor, current, input, _ := kubernetesReportingFixture(t)
			collected := false
			executor.controller = ensureReportJobFunc(func(
				context.Context,
				string,
				*batchv1.Job,
			) (kubernetes.Observation, error) {
				return kubernetes.Observation{Phase: test.phase}, nil
			})
			executor.collector = collectReportFunc(func(
				context.Context,
				string,
				run.Run,
				ReportingInput,
			) (run.Artifact, error) {
				collected = true
				return run.Artifact{}, nil
			})
			if _, err := executor.Report(
				context.Background(),
				"principal-a",
				current,
				input,
			); !errors.Is(err, test.want) {
				t.Fatalf("phase %s error = %v, want %v", test.phase, err, test.want)
			}
			if collected {
				t.Fatal("non-successful Job triggered output collection")
			}
		})
	}
}

func TestKubernetesReportExecutorPreservesBoundaryErrors(t *testing.T) {
	for _, boundary := range []string{"resolver", "controller", "collector"} {
		t.Run(boundary, func(t *testing.T) {
			executor, current, input, _ := kubernetesReportingFixture(t)
			want := run.ErrUnavailable
			switch boundary {
			case "resolver":
				executor.resolver = resolveReportJobFunc(func(
					context.Context, string, run.Run, ReportingInput,
				) (*batchv1.Job, error) {
					return nil, want
				})
			case "controller":
				executor.controller = ensureReportJobFunc(func(
					context.Context, string, *batchv1.Job,
				) (kubernetes.Observation, error) {
					return kubernetes.Observation{}, want
				})
			case "collector":
				executor.collector = collectReportFunc(func(
					context.Context, string, run.Run, ReportingInput,
				) (run.Artifact, error) {
					return run.Artifact{}, want
				})
			}
			if _, err := executor.Report(
				context.Background(),
				"principal-a",
				current,
				input,
			); !errors.Is(err, want) {
				t.Fatalf("%s error = %v", boundary, err)
			}
		})
	}
}

func TestKubernetesReportExecutorValidatesDependenciesAndTemplate(t *testing.T) {
	executor, current, input, _ := kubernetesReportingFixture(t)
	executor.resolver = resolveReportJobFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (*batchv1.Job, error) {
		return nil, nil
	})
	if _, err := executor.Report(
		context.Background(),
		"principal-a",
		current,
		input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil template error = %v", err)
	}

	valid, _, _, _ := kubernetesReportingFixture(t)
	for name, dependencies := range map[string][]any{
		"resolver":   {nil, valid.controller, valid.collector},
		"controller": {valid.resolver, nil, valid.collector},
		"collector":  {valid.resolver, valid.controller, nil},
	} {
		t.Run(name, func(t *testing.T) {
			resolver, _ := dependencies[0].(ReportJobResolver)
			controller, _ := dependencies[1].(ReportJobController)
			collector, _ := dependencies[2].(ReportArtifactCollector)
			if _, err := NewKubernetesReportExecutor(
				resolver,
				controller,
				collector,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("missing dependency error = %v", err)
			}
		})
	}
}
