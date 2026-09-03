package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type memoryPods struct {
	list    *corev1.PodList
	err     error
	options metav1.ListOptions
}

func (pods *memoryPods) List(
	ctx context.Context,
	options metav1.ListOptions,
) (*corev1.PodList, error) {
	pods.options = options
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return pods.list, pods.err
}

func executionPod(execution Execution) corev1.Pod {
	controller := true

	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "runner-pod",
		Namespace: execution.Namespace,
		Labels: map[string]string{
			runLabel:       execution.RunID,
			managedByLabel: managedByValue,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1",
			Kind:       "Job",
			Name:       execution.JobName,
			UID:        execution.UID,
			Controller: &controller,
		}},
	}}
}

func TestStopVerifierRequiresAllOwnedPodsToDisappear(t *testing.T) {
	execution := Execution{
		RunID: testRunID, Namespace: "perf-runs", JobName: testRunID,
		UID: types.UID("job-uid"), SpecSHA256: strings.Repeat("a", 64),
	}
	pods := &memoryPods{list: &corev1.PodList{Items: []corev1.Pod{executionPod(execution)}}}
	verifier, err := NewStopVerifier(pods, execution.Namespace)
	if err != nil {
		t.Fatal(err)
	}

	stopped, err := verifier.ConfirmExecutionStopped(context.Background(), execution)
	if err != nil || stopped {
		t.Fatalf("owned Pod treated as stopped: %t, %v", stopped, err)
	}
	if !strings.Contains(pods.options.LabelSelector, runLabel+"="+execution.RunID) ||
		!strings.Contains(pods.options.LabelSelector, managedByLabel+"="+managedByValue) {
		t.Fatalf("unsafe Pod selector %q", pods.options.LabelSelector)
	}

	pods.list.Items = nil
	stopped, err = verifier.ConfirmExecutionStopped(context.Background(), execution)
	if err != nil || !stopped {
		t.Fatalf("empty owned-Pod list not confirmed: %t, %v", stopped, err)
	}
}

func TestStopVerifierRejectsConflictingPodIdentity(t *testing.T) {
	execution := Execution{
		RunID: testRunID, Namespace: "perf-runs", JobName: testRunID,
		UID: types.UID("job-uid"), SpecSHA256: strings.Repeat("a", 64),
	}
	valid := executionPod(execution)
	conflict := executionPod(execution)
	conflict.OwnerReferences[0].UID = types.UID("replacement")
	pods := &memoryPods{list: &corev1.PodList{Items: []corev1.Pod{valid, conflict}}}
	verifier, err := NewStopVerifier(pods, execution.Namespace)
	if err != nil {
		t.Fatal(err)
	}

	if stopped, err := verifier.ConfirmExecutionStopped(context.Background(), execution); stopped || !errors.Is(err, ErrJobConflict) {
		t.Fatalf("conflicting Pod = %t, %v", stopped, err)
	}
}

func TestStopVerifierClassifiesSafeErrors(t *testing.T) {
	execution := Execution{
		RunID: testRunID, Namespace: "perf-runs", JobName: testRunID,
		UID: types.UID("job-uid"), SpecSHA256: strings.Repeat("a", 64),
	}
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"unavailable", apierrors.NewServiceUnavailable("token=secret"), run.ErrUnavailable},
		{
			"forbidden",
			apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "secret-pod", errors.New("token=secret")),
			nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewStopVerifier(&memoryPods{err: test.err}, execution.Namespace)
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifier.ConfirmExecutionStopped(context.Background(), execution)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("missing or unsafe error: %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.want == nil && errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("permanent error classified as retryable: %v", err)
			}
		})
	}

	verifier, err := NewStopVerifier(&memoryPods{}, execution.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.ConfirmExecutionStopped(context.Background(), execution); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("nil Pod list error = %v", err)
	}
}

func TestStopVerifierValidatesDependenciesAndExecution(t *testing.T) {
	if _, err := NewStopVerifier(nil, "perf-runs"); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil Pods error = %v", err)
	}
	if _, err := NewStopVerifier(&memoryPods{}, "Invalid"); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid namespace error = %v", err)
	}

	verifier, err := NewStopVerifier(&memoryPods{}, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.ConfirmExecutionStopped(context.Background(), Execution{}); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid execution error = %v", err)
	}
}
