package app

import (
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestNewKubernetesControllersComposesAllAdaptersWithoutRequests(t *testing.T) {
	clients, kubernetesClient := namespacedKubernetesClients()

	controllers, err := NewKubernetesControllers(clients)
	if err != nil {
		t.Fatal(err)
	}
	if controllers.Execution == nil || controllers.Termination == nil ||
		controllers.Analysis == nil || controllers.Reporting == nil {
		t.Fatalf("controllers = %#v", controllers)
	}
	if actions := kubernetesClient.Actions(); len(actions) != 0 {
		t.Fatalf("construction made Kubernetes requests: %#v", actions)
	}
}

func TestNewKubernetesControllersRejectsInvalidClients(t *testing.T) {
	valid, _ := namespacedKubernetesClients()
	tests := map[string]KubernetesClients{
		"empty": {},
		"namespace": {
			Namespace: "Invalid_Namespace",
			Jobs:      valid.Jobs,
			Pods:      valid.Pods,
		},
		"Jobs": {
			Namespace: valid.Namespace,
			Pods:      valid.Pods,
		},
		"Pods": {
			Namespace: valid.Namespace,
			Jobs:      valid.Jobs,
		},
	}
	for name, clients := range tests {
		t.Run(name, func(t *testing.T) {
			controllers, err := NewKubernetesControllers(clients)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("controllers = %#v, error = %v", controllers, err)
			}
		})
	}
}

func namespacedKubernetesClients() (KubernetesClients, *fake.Clientset) {
	const namespace = "perf-runs"

	kubernetesClient := fake.NewSimpleClientset()

	return KubernetesClients{
		Namespace: namespace,
		Jobs:      kubernetesClient.BatchV1().Jobs(namespace),
		Pods:      kubernetesClient.CoreV1().Pods(namespace),
	}, kubernetesClient
}
