package kubernetes

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchclient "k8s.io/client-go/kubernetes/typed/batch/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const disposableKubernetesContext = "kind-perfeng-test"

func TestDispatcherAgainstAPIServer(t *testing.T) {
	kubeconfig := os.Getenv("PERFENG_TEST_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("PERFENG_TEST_KUBECONFIG is not set")
	}
	config := loadDisposableKubernetesConfig(t, kubeconfig)
	core, err := coreclient.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := batchclient.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	namespace, err := core.Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "perfeng-control-plane-test-"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := core.Namespaces().Delete(
			context.Background(), namespace.Name, metav1.DeleteOptions{},
		); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})

	dispatcher, err := NewDispatcher(batch.Jobs(namespace.Name), namespace.Name)
	if err != nil {
		t.Fatal(err)
	}
	template := jobTemplate()
	template.Spec.Template.Spec.NodeSelector = map[string]string{
		"perfeng.io/integration-test": "unschedulable",
	}
	created, err := dispatcher.EnsureJob(ctx, testRunID, template)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.UID == "" {
		t.Fatal("API server did not return the created Job identity")
	}
	stored, err := batch.Jobs(namespace.Name).Get(ctx, created.JobName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.CompletionMode == nil || *stored.Spec.CompletionMode != batchv1.NonIndexedCompletion ||
		stored.Spec.PodReplacementPolicy == nil ||
		*stored.Spec.PodReplacementPolicy != batchv1.TerminatingOrFailed ||
		stored.Spec.Template.Spec.DNSPolicy != corev1.DNSClusterFirst ||
		stored.Spec.Template.Spec.Containers[0].TerminationMessagePath == "" {
		t.Fatal("API server did not apply the defaults covered by this test")
	}

	adopted, err := dispatcher.EnsureJob(ctx, testRunID, template)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Created || adopted.UID != created.UID || adopted.SpecSHA256 != created.SpecSHA256 {
		t.Fatal("API-server-defaulted Job was not adopted")
	}
	if _, err := dispatcher.ObserveJob(ctx, adopted.Execution()); err != nil {
		t.Fatal(err)
	}
}

func loadDisposableKubernetesConfig(t *testing.T, kubeconfig string) *rest.Config {
	t.Helper()
	rawConfig, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if rawConfig.CurrentContext != disposableKubernetesContext {
		t.Fatalf("Kubernetes integration requires context %q, found %q", disposableKubernetesContext, rawConfig.CurrentContext)
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if !isLoopbackKubernetesHost(config.Host) {
		t.Fatalf("Kubernetes integration requires a loopback API server, found %q", config.Host)
	}

	return config
}

func isLoopbackKubernetesHost(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "https" {
		return false
	}
	host := endpoint.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func TestLoopbackKubernetesHost(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		host string
		want bool
	}{
		{"https://127.0.0.1:6443", true},
		{"https://[::1]:6443", true},
		{"https://localhost:6443", true},
		{"https://cluster.example:6443", false},
		{"http://127.0.0.1:8080", false},
		{"not a URL", false},
	} {
		if got := isLoopbackKubernetesHost(test.host); got != test.want {
			t.Errorf("isLoopbackKubernetesHost(%q) = %t, want %t", test.host, got, test.want)
		}
	}
}
