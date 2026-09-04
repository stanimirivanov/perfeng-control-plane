package kubernetes

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestReportingDispatcherCreatesAndAdoptsDeterministicJob(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewReportingDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}

	first, err := dispatcher.EnsureReportJob(context.Background(), testRunID, jobTemplate())
	if err != nil || first.Phase != JobPending {
		t.Fatalf("first dispatch = %+v, %v", first, err)
	}
	if jobs.job.Name != testRunID+reportJobSuffix ||
		jobs.job.Labels[runLabel] != testRunID ||
		jobs.job.Labels[stageLabel] != reportingStage ||
		jobs.job.Spec.Template.Labels[stageLabel] != reportingStage {
		t.Fatalf("reporting identity = %+v", jobs.job.ObjectMeta)
	}
	uid := jobs.job.UID

	second, err := dispatcher.EnsureReportJob(context.Background(), testRunID, jobTemplate())
	if err != nil || second.Phase != JobPending || jobs.job.UID != uid {
		t.Fatalf("adopted dispatch = %+v, %v", second, err)
	}
}

func TestReportingDispatcherRejectsConflictingJob(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewReportingDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureReportJob(context.Background(), testRunID, jobTemplate()); err != nil {
		t.Fatal(err)
	}
	jobs.job.Spec.Template.Spec.Containers[0].Args = []string{"changed"}
	if _, err := dispatcher.EnsureReportJob(
		context.Background(),
		testRunID,
		jobTemplate(),
	); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestReportingDispatcherRecoversAmbiguousCreate(t *testing.T) {
	jobs := &memoryJobs{storeThenError: apierrors.NewServerTimeout(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"create",
		1,
	)}
	dispatcher, err := NewReportingDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.EnsureReportJob(
		context.Background(),
		testRunID,
		jobTemplate(),
	); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("ambiguous create error = %v", err)
	}
	if _, err := dispatcher.EnsureReportJob(
		context.Background(),
		testRunID,
		jobTemplate(),
	); err != nil {
		t.Fatal("persisted report Job was not adopted", err)
	}
}

func TestReportingDispatcherValidatesIdentityAndConfiguration(t *testing.T) {
	jobs := &memoryJobs{}
	dispatcher, err := NewReportingDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	foreign := jobTemplate()
	foreign.Labels = map[string]string{stageLabel: analysisStage}
	for name, test := range map[string]struct {
		runID    string
		template bool
		foreign  bool
	}{
		"invalid run":   {runID: "invalid", template: true},
		"nil template":  {runID: testRunID},
		"foreign stage": {runID: testRunID, template: true, foreign: true},
	} {
		t.Run(name, func(t *testing.T) {
			var template = jobTemplate()
			if !test.template {
				template = nil
			} else if test.foreign {
				template = foreign
			}
			if _, err := dispatcher.EnsureReportJob(
				context.Background(),
				test.runID,
				template,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
	if _, err := NewReportingDispatcher(nil, "perf-runs"); err == nil {
		t.Fatal("nil Jobs client accepted")
	}
	if _, err := NewReportingDispatcher(jobs, "Invalid_Namespace"); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}
