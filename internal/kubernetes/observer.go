package kubernetes

import (
	"context"
	"errors"
	"regexp"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// JobPhase is the observed Kubernetes execution phase without interpreting test
// results, artifact completeness or measurement quality.
type JobPhase string

// Terminal phases require a true Complete or Failed condition. Status counters
// and interim conditions alone do not prove that Kubernetes ended the Job.
const (
	JobAbsent    JobPhase = "ABSENT"
	JobPending   JobPhase = "PENDING"
	JobRunning   JobPhase = "RUNNING"
	JobSucceeded JobPhase = "SUCCEEDED"
	JobFailed    JobPhase = "FAILED"
)

// Observation is an identity-checked snapshot of Kubernetes Job status.
type Observation struct {
	Phase      JobPhase
	Active     int32
	Succeeded  int32
	Failed     int32
	Deleting   bool
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// ObserveJob reads the exact dispatched Job. A replacement, changed fingerprint
// or changed execution specification returns ErrJobConflict rather than being
// interpreted as this Run's workload.
func (dispatcher *Dispatcher) ObserveJob(
	ctx context.Context,
	dispatch Dispatch,
) (Observation, error) {
	if !dispatcher.validDispatch(dispatch) {
		return Observation{}, run.ErrValidation
	}

	job, err := dispatcher.jobs.Get(ctx, dispatch.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Observation{Phase: JobAbsent}, nil
	}
	if err != nil {
		return Observation{}, classifyAPIError("get", err)
	}
	if err := matchingDispatch(job, dispatch); err != nil {
		return Observation{}, err
	}

	return observe(job), nil
}

// RequestJobStop submits foreground deletion for the exact observed Job UID.
// Success means deletion was accepted or the Job was already absent; callers
// must separately confirm Pod termination and prevent stale creates before
// treating cancellation as complete. This method does not mutate Run state.
func (dispatcher *Dispatcher) RequestJobStop(ctx context.Context, dispatch Dispatch) error {
	if !dispatcher.validDispatch(dispatch) {
		return run.ErrValidation
	}

	job, err := dispatcher.jobs.Get(ctx, dispatch.JobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return classifyAPIError("get", err)
	}
	if err := matchingDispatch(job, dispatch); err != nil {
		return err
	}

	policy := metav1.DeletePropagationForeground
	err = dispatcher.jobs.Delete(ctx, dispatch.JobName, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &dispatch.UID},
		PropagationPolicy: &policy,
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if apierrors.IsConflict(err) {
		return ErrJobConflict
	}
	if err != nil {
		return classifyAPIError("delete", err)
	}

	return nil
}

func (dispatcher *Dispatcher) validDispatch(dispatch Dispatch) bool {
	return contract.ValidID(dispatch.RunID) &&
		dispatch.JobName == dispatch.RunID &&
		dispatch.Namespace == dispatcher.namespace &&
		dispatch.UID != "" &&
		fingerprintPattern.MatchString(dispatch.SpecSHA256)
}

func matchingDispatch(job *batchv1.Job, dispatch Dispatch) error {
	if job == nil ||
		job.Name != dispatch.JobName ||
		job.Namespace != dispatch.Namespace ||
		job.UID != dispatch.UID ||
		job.Labels[runLabel] != dispatch.RunID ||
		job.Labels[managedByLabel] != managedByValue ||
		job.Annotations[specAnnotation] != dispatch.SpecSHA256 {
		return ErrJobConflict
	}

	fingerprint, err := jobFingerprint(job)
	if err != nil {
		return errors.New("could not verify Kubernetes Job specification")
	}
	if fingerprint != dispatch.SpecSHA256 {
		return ErrJobConflict
	}

	return nil
}

func observe(job *batchv1.Job) Observation {
	observation := Observation{
		Phase:     JobPending,
		Active:    job.Status.Active,
		Succeeded: job.Status.Succeeded,
		Failed:    job.Status.Failed,
		Deleting:  job.DeletionTimestamp != nil,
		StartedAt: timeValue(job.Status.StartTime),
	}
	if observation.StartedAt != nil || observation.Active > 0 {
		observation.Phase = JobRunning
	}

	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobFailed:
			observation.Phase = JobFailed
			observation.FinishedAt = timeValue(condition.LastTransitionTime.DeepCopy())
		case batchv1.JobComplete:
			if observation.Phase != JobFailed {
				observation.Phase = JobSucceeded
				observation.FinishedAt = timeValue(condition.LastTransitionTime.DeepCopy())
			}
		}
	}

	return observation
}

func timeValue(value *metav1.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.Time.UTC()

	return &result
}
