package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func dispatchedJob(t *testing.T) (*Dispatcher, *memoryJobs, Dispatch) {
	t.Helper()
	client := &memoryJobs{}
	dispatcher := newTestDispatcher(t, client)
	dispatch, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate())
	if err != nil {
		t.Fatal(err)
	}

	return dispatcher, client, dispatch
}

func TestObserveJobPhases(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	started := metav1.NewTime(time.Date(2026, 9, 3, 12, 0, 0, 0, time.FixedZone("test", 3600)))
	finished := metav1.NewTime(started.Add(time.Minute))

	for name, test := range map[string]struct {
		status batchv1.JobStatus
		phase  JobPhase
	}{
		"pending":             {batchv1.JobStatus{}, JobPending},
		"active-before-start": {batchv1.JobStatus{Active: 1}, JobRunning},
		"running":             {batchv1.JobStatus{StartTime: &started, Active: 1}, JobRunning},
		"succeeded": {batchv1.JobStatus{
			StartTime: &started, Succeeded: 1, Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: finished,
			}},
		}, JobSucceeded},
		"failed": {batchv1.JobStatus{
			StartTime: &started, Failed: 1, Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: finished,
			}},
		}, JobFailed},
	} {
		t.Run(name, func(t *testing.T) {
			client.job.Status = test.status
			observation, err := dispatcher.ObserveJob(context.Background(), dispatch)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Phase != test.phase {
				t.Fatalf("got phase %s, want %s", observation.Phase, test.phase)
			}
			if observation.StartedAt != nil && observation.StartedAt.Location() != time.UTC {
				t.Fatal("start time was not normalized to UTC")
			}
			if (test.phase == JobSucceeded || test.phase == JobFailed) && observation.FinishedAt == nil {
				t.Fatal("terminal observation has no finish time")
			}
		})
	}
}

func TestObserveJobAbsentAndIdentityConflicts(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	client.job = nil
	observation, err := dispatcher.ObserveJob(context.Background(), dispatch)
	if err != nil || observation.Phase != JobAbsent {
		t.Fatal("absent Job was not reported", err)
	}

	dispatcher, client, dispatch = dispatchedJob(t)
	client.job.UID = types.UID("replacement")
	if _, err := dispatcher.ObserveJob(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
		t.Fatal("replacement Job was observed as the dispatched workload", err)
	}

	dispatcher, client, dispatch = dispatchedJob(t)
	client.job.Spec.Template.Spec.Containers[0].Args = []string{"changed"}
	if _, err := dispatcher.ObserveJob(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
		t.Fatal("changed Job specification was observed", err)
	}
}

func TestRequestJobStopUsesUIDAndIsIdempotent(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	if err := dispatcher.RequestJobStop(context.Background(), dispatch); err != nil {
		t.Fatal(err)
	}
	if client.deletes != 1 || client.deleteOptions.Preconditions == nil ||
		client.deleteOptions.Preconditions.UID == nil ||
		*client.deleteOptions.Preconditions.UID != dispatch.UID ||
		client.deleteOptions.PropagationPolicy == nil ||
		*client.deleteOptions.PropagationPolicy != metav1.DeletePropagationForeground {
		t.Fatal("stop did not use foreground deletion with the dispatched UID")
	}

	if err := dispatcher.RequestJobStop(context.Background(), dispatch); err != nil {
		t.Fatal("repeated stop was not idempotent", err)
	}
	if client.deletes != 1 {
		t.Fatal("absent Job triggered another delete")
	}
}

func TestRequestJobStopNeverDeletesReplacement(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	client.job.UID = types.UID("replacement")
	if err := dispatcher.RequestJobStop(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
		t.Fatal("replacement Job did not produce a conflict", err)
	}
	if client.deletes != 0 || client.job == nil {
		t.Fatal("replacement Job was deleted")
	}
}

func TestRequestJobStopRejectsReplacementAfterRead(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	client.beforeDelete = func(job *batchv1.Job) { job.UID = types.UID("replacement") }
	if err := dispatcher.RequestJobStop(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
		t.Fatal("UID precondition did not reject replacement after read", err)
	}
	if client.deletes != 1 || client.job == nil || client.job.UID != "replacement" {
		t.Fatal("replacement Job was deleted")
	}
}

func TestRequestJobStopDoesNotReportImmediateAbsence(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	client.keepDeleting = true
	if err := dispatcher.RequestJobStop(context.Background(), dispatch); err != nil {
		t.Fatal(err)
	}
	observation, err := dispatcher.ObserveJob(context.Background(), dispatch)
	if err != nil || !observation.Deleting || observation.Phase == JobAbsent {
		t.Fatal("accepted deletion was treated as completed deletion", err)
	}
}

func TestObservationErrorsAndDispatchValidation(t *testing.T) {
	dispatcher, client, dispatch := dispatchedJob(t)
	invalid := dispatch
	invalid.SpecSHA256 = "invalid"
	if _, err := dispatcher.ObserveJob(context.Background(), invalid); !errors.Is(err, run.ErrValidation) {
		t.Fatal("invalid dispatch was observed", err)
	}
	if err := dispatcher.RequestJobStop(context.Background(), invalid); !errors.Is(err, run.ErrValidation) {
		t.Fatal("invalid dispatch was stopped", err)
	}

	client.deleteError = apierrors.NewServerTimeout(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"delete",
		1,
	)
	if err := dispatcher.RequestJobStop(context.Background(), dispatch); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("transient delete was not classified as unavailable", err)
	}
}

func TestObserveJobRequiresTerminalCondition(t *testing.T) {
	for name, condition := range map[string]batchv1.JobCondition{
		"false-complete":   {Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
		"unknown-failed":   {Type: batchv1.JobFailed, Status: corev1.ConditionUnknown},
		"failure-target":   {Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
		"success-criteria": {Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher, client, dispatch := dispatchedJob(t)
			client.job.Status = batchv1.JobStatus{
				Active: 1, Failed: 1, Succeeded: 1,
				Conditions: []batchv1.JobCondition{condition},
			}
			observation, err := dispatcher.ObserveJob(context.Background(), dispatch)
			if err != nil || observation.Phase != JobRunning || observation.FinishedAt != nil {
				t.Fatal("nonterminal status was treated as terminal", observation, err)
			}
			if observation.Active != 1 || observation.Failed != 1 || observation.Succeeded != 1 {
				t.Fatal("status counters were not preserved", observation)
			}
		})
	}
}

func TestObserveAndStopRejectChangedIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*batchv1.Job){
		"namespace":     func(job *batchv1.Job) { job.Namespace = "other" },
		"owner":         func(job *batchv1.Job) { job.Labels[managedByLabel] = "other" },
		"run":           func(job *batchv1.Job) { delete(job.Labels, runLabel) },
		"fingerprint":   func(job *batchv1.Job) { job.Annotations[specAnnotation] = strings.Repeat("a", 64) },
		"specification": func(job *batchv1.Job) { job.Spec.Template.Spec.Containers[0].Args = []string{"changed"} },
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher, client, dispatch := dispatchedJob(t)
			mutate(client.job)
			if _, err := dispatcher.ObserveJob(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
				t.Fatal("changed identity was observed", err)
			}
			if err := dispatcher.RequestJobStop(context.Background(), dispatch); !errors.Is(err, ErrJobConflict) {
				t.Fatal("changed identity was stopped", err)
			}
			if client.deletes != 0 {
				t.Fatal("conflicting Job triggered deletion")
			}
		})
	}
}

func TestObserveAndStopPreserveSafeErrors(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"cancelled":   {context.Canceled, context.Canceled},
		"deadline":    {context.DeadlineExceeded, context.DeadlineExceeded},
		"unavailable": {apierrors.NewServiceUnavailable("token=secret"), run.ErrUnavailable},
		"forbidden":   {apierrors.NewForbidden(schema.GroupResource{Resource: "jobs"}, "secret-job", errors.New("token=secret")), nil},
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher, client, dispatch := dispatchedJob(t)
			client.getError = test.err
			_, observeErr := dispatcher.ObserveJob(context.Background(), dispatch)
			stopErr := dispatcher.RequestJobStop(context.Background(), dispatch)
			client.getError = nil
			client.deleteError = test.err
			deleteErr := dispatcher.RequestJobStop(context.Background(), dispatch)
			for _, err := range []error{observeErr, stopErr, deleteErr} {
				if err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatal("missing or unsafe API error", err)
				}
				if test.want != nil && !errors.Is(err, test.want) {
					t.Fatal("error identity was lost", err)
				}
				if test.want == nil && errors.Is(err, run.ErrUnavailable) {
					t.Fatal("permanent API error was classified as retryable", err)
				}
			}
		})
	}
}
