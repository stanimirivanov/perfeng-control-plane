package kubernetes

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestBenchmarkAgainstAPIServer(t *testing.T) {
	kubeconfig := os.Getenv("PERFENG_TEST_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("PERFENG_TEST_KUBECONFIG is not set")
	}
	client, err := clientset.NewForConfig(loadDisposableKubernetesConfig(t, kubeconfig))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Fixed executor placement is part of the contained protocol. The guarded
	// disposable cluster must not contain someone else's namespace of that name.
	if _, err := client.CoreV1().Namespaces().Get(ctx, "perf-generators", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("disposable executor namespace must initially be absent")
	}
	namespace, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "perf-generators"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.CoreV1().Namespaces().Delete(ctx, namespace.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &namespace.UID}}); err != nil {
			t.Error(err)
		}
	})
	_, _, spec, _ := benchmarkFixture(t)
	kubeSystem, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec.ClusterUID = string(kubeSystem.UID)
	reservation := benchmarkReservation(t, spec)
	controller, err := NewBenchmarkController(client, func(context.Context) ([]byte, error) { return nil, errors.New("not used by resource protocol test") }, namespace.Name, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	fence := func(context.Context) error { return nil }
	uid, err := controller.EnsureNamespace(ctx, reservation, fence)
	if err != nil {
		t.Fatal(err)
	}
	reservation.NamespaceUID = uid
	target, err := client.CoreV1().Namespaces().Get(ctx, benchmark.Namespace(testRunID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.CoreV1().Namespaces().Delete(ctx, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &target.UID}}); err != nil {
			t.Error(err)
		}
	})
	reservation.Request, err = benchmark.RequestBytes(spec, reservation.RunID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureResources(ctx, spec, reservation, fence); err != nil {
		t.Fatal(err)
	}
	name := benchmark.ResourceName(testRunID)
	role, err := client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &role.UID}}); err != nil {
			t.Error(err)
		}
	})
	binding, err := client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &binding.UID}}); err != nil {
			if !apierrors.IsNotFound(err) {
				t.Error(err)
			}
		}
	})
	if err := controller.EnsureResources(ctx, spec, reservation, fence); err != nil {
		t.Fatalf("API-defaulted objects were not adopted: %v", err)
	}
	template := BenchmarkJobTemplate(spec.RunnerImage, "registry", "storage", "http://seaweedfs-s3.perf-platform.svc.cluster.local:8333")
	job, err := BindBenchmarkJob(template, spec, reservation)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise Job admission/defaulting without starting or pulling either image.
	job.Spec.Suspend = ptr.To(true)
	dispatcher, err := NewDispatcher(client.BatchV1().Jobs(namespace.Name), namespace.Name)
	if err != nil {
		t.Fatal(err)
	}
	created, err := dispatcher.EnsureJob(ctx, testRunID, job)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := client.BatchV1().Jobs(namespace.Name).Get(ctx, created.JobName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := stored.Spec.Template.Spec
	if pod.ServiceAccountName != name || pod.DeprecatedServiceAccount != pod.ServiceAccountName {
		t.Fatal("API server did not return the expected service-account alias")
	}
	adopted, err := dispatcher.EnsureJob(ctx, testRunID, job)
	if err != nil || adopted.Created || adopted.Execution() != created.Execution() {
		t.Fatalf("contained Job not adopted: %v", err)
	}
	if _, err := dispatcher.ObserveJob(ctx, adopted.Execution()); err != nil {
		t.Fatalf("API-returned contained Job could not be observed: %v", err)
	}
	inputs, err := client.CoreV1().ConfigMaps(namespace.Name).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Immutable == nil || !*inputs.Immutable || inputs.Data["request.json"] != string(reservation.Request) {
		t.Fatal("API lost sealed inputs")
	}
	inputs.Data["request.json"] = "changed"
	if _, err := client.CoreV1().ConfigMaps(namespace.Name).Update(ctx, inputs, metav1.UpdateOptions{}); !apierrors.IsInvalid(err) {
		t.Fatalf("API admitted immutable input mutation: %v", err)
	}
	reservation.Cleanup = &run.BenchmarkCleanup{}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		err := controller.CloseBenchmarkTarget(ctx, reservation, fence)
		if errors.Is(err, run.ErrUnavailable) {
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		t.Fatalf("API did not confirm closed quota: %v", err)
	}
	probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "quota-probe", Namespace: target.Name}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: template.Spec.Template.Spec.SecurityContext.DeepCopy(),
		Containers: []corev1.Container{{Name: "pause", Image: benchmark.CandidateImage,
			SecurityContext: template.Spec.Template.Spec.Containers[0].SecurityContext.DeepCopy(),
			Resources:       benchmarkResources("1m", "1Mi", "64Ki", "1m", "1Mi", "64Ki")}},
	}}
	if _, err := client.CoreV1().Pods(target.Name).Create(ctx, probe, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); !apierrors.IsForbidden(err) ||
		!strings.Contains(err.Error(), "exceeded quota: benchmark-budget") || !strings.Contains(err.Error(), "pods=0") {
		t.Fatalf("closed quota did not reject Pod admission: %v", err)
	}
	objects := benchmarkTemplates(namespace.Name, spec, reservation)
	if absent, err := deleteBenchmarkObject(ctx, client.RbacV1().ClusterRoleBindings(), objects.clusterBinding, fence); err != nil || absent {
		t.Fatalf("UID-preconditioned revoke: %v", err)
	}
	if absent, err := deleteBenchmarkObject(ctx, client.RbacV1().ClusterRoleBindings(), objects.clusterBinding, fence); err != nil || !absent {
		t.Fatalf("revoke retry did not observe absence: %v", err)
	}
}
