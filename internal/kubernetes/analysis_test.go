package kubernetes

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestAnalysisDispatcherCreatesAndAdoptsDeterministicJob(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}

	first, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate())
	if err != nil || first.Phase != JobPending {
		t.Fatalf("first dispatch = %+v, %v", first, err)
	}
	if jobs.job.Name != testRunID+analysisJobSuffix ||
		jobs.job.Labels[runLabel] != testRunID || jobs.job.Labels[stageLabel] != analysisStage ||
		jobs.job.Spec.Template.Labels[stageLabel] != analysisStage {
		t.Fatalf("analysis identity = %+v", jobs.job.ObjectMeta)
	}
	uid := jobs.job.UID

	second, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate())
	if err != nil || second.Phase != JobPending || jobs.job.UID != uid {
		t.Fatalf("adopted dispatch = %+v, %v", second, err)
	}
}

func TestAnalysisDispatcherReportsTerminalJobPhase(t *testing.T) {
	for name, condition := range map[string]batchv1.JobConditionType{
		"succeeded": batchv1.JobComplete,
		"failed":    batchv1.JobFailed,
	} {
		t.Run(name, func(t *testing.T) {
			jobs := &memoryJobs{mutateStored: func(job *batchv1.Job) {
				job.Status.Conditions = []batchv1.JobCondition{{
					Type: condition, Status: corev1.ConditionTrue,
				}}
			}}
			dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
			if err != nil {
				t.Fatal(err)
			}
			observation, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate())
			if err != nil {
				t.Fatal(err)
			}
			want := JobSucceeded
			if condition == batchv1.JobFailed {
				want = JobFailed
			}
			if observation.Phase != want {
				t.Fatalf("phase = %s, want %s", observation.Phase, want)
			}
		})
	}
}

func TestAnalysisDispatcherRejectsConflictingJob(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate()); err != nil {
		t.Fatal(err)
	}
	jobs.job.Spec.Template.Spec.Containers[0].Args = []string{"changed"}
	if _, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate()); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestAnalysisDispatcherRecoversAmbiguousCreate(t *testing.T) {
	jobs := &memoryJobs{storeThenError: apierrors.NewServerTimeout(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"create",
		1,
	)}
	dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate()); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("ambiguous create error = %v", err)
	}
	if _, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate()); err != nil {
		t.Fatal("persisted analysis Job was not adopted", err)
	}
}

func TestAnalysisDispatcherValidatesIdentityAndConfiguration(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	foreign := jobTemplate()
	foreign.Labels = map[string]string{stageLabel: "execution"}
	presetName := jobTemplate()
	presetName.Name = testRunID
	for name, test := range map[string]struct {
		runID    string
		template *batchv1.Job
	}{
		"invalid run":    {"invalid", jobTemplate()},
		"nil template":   {testRunID, nil},
		"foreign stage":  {testRunID, foreign},
		"execution name": {testRunID, presetName},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dispatcher.EnsureAnalysisJob(context.Background(), test.runID, test.template); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
	if _, err := NewAnalysisDispatcher(nil, "perf-runs"); err == nil {
		t.Fatal("nil Jobs client accepted")
	}
	if _, err := NewAnalysisDispatcher(jobs, "Invalid_Namespace"); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}

func TestAnalysisDispatcherPreservesSafeAPIErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	jobs := &memoryJobs{createError: context.Canceled}
	dispatcher, err := NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureAnalysisJob(cancelled, testRunID, jobTemplate()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	jobs = &memoryJobs{createError: apierrors.NewForbidden(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"secret-job",
		errors.New("token=secret"),
	)}
	dispatcher, err = NewAnalysisDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureAnalysisJob(context.Background(), testRunID, jobTemplate()); err == nil || errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("API error = %v", err)
	}
}
