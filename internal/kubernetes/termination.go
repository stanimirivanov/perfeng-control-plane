package kubernetes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Pods is the Kubernetes API boundary needed to verify workload termination.
// A typed client-go PodInterface satisfies this contract.
type Pods interface {
	List(context.Context, metav1.ListOptions) (*corev1.PodList, error)
}

// StopVerifier checks dependent Pods after an identity-checked foreground Job
// deletion. It does not submit deletion requests itself.
type StopVerifier struct {
	pods      Pods
	namespace string
}

// NewStopVerifier binds termination checks to one namespace.
func NewStopVerifier(pods Pods, namespace string) (*StopVerifier, error) {
	if pods == nil || len(validation.IsDNS1123Label(namespace)) != 0 {
		return nil, run.ErrValidation
	}

	return &StopVerifier{pods: pods, namespace: namespace}, nil
}

// ConfirmExecutionStopped returns true only when no Pod carrying the execution's
// control-plane identity remains. Conflicting ownership is never ignored.
func (verifier *StopVerifier) ConfirmExecutionStopped(
	ctx context.Context,
	execution Execution,
) (bool, error) {
	if !execution.Valid() || execution.Namespace != verifier.namespace {
		return false, run.ErrValidation
	}

	selector := labels.Set{
		runLabel:       execution.RunID,
		managedByLabel: managedByValue,
	}.AsSelector().String()
	pods, err := verifier.pods.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, classifyAPIError("list Pods", err)
	}
	if pods == nil {
		return false, run.ErrUnavailable
	}
	found := false
	for index := range pods.Items {
		pod := &pods.Items[index]
		owner := metav1.GetControllerOf(pod)
		if pod.Namespace != execution.Namespace ||
			pod.Labels[runLabel] != execution.RunID ||
			pod.Labels[managedByLabel] != managedByValue ||
			owner == nil || owner.APIVersion != "batch/v1" || owner.Kind != "Job" ||
			owner.Name != execution.JobName ||
			owner.UID != execution.UID {
			return false, ErrJobConflict
		}
		found = true
	}

	return !found, nil
}
