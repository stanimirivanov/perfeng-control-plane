package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func cleanupFixture(t *testing.T) (*BenchmarkController, *fake.Clientset, benchmark.Spec, run.BenchmarkReservation) {
	t.Helper()
	controller, client, spec, _ := benchmarkFixture(t)
	reservation := benchmarkReservation(t, spec)
	fence := func(context.Context) error { return nil }
	uid, err := controller.EnsureNamespace(context.Background(), reservation, fence)
	if err != nil {
		t.Fatal(err)
	}
	reservation.NamespaceUID = uid
	reservation.Request, err = benchmark.RequestBytes(spec, reservation.RunID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureResources(context.Background(), spec, reservation, fence); err != nil {
		t.Fatal(err)
	}
	reservation.Cleanup = &run.BenchmarkCleanup{Ready: true}
	return controller, client, spec, reservation
}

func confirmZeroQuota(t *testing.T, controller *BenchmarkController, client *fake.Clientset, reservation run.BenchmarkReservation) {
	t.Helper()
	fence := func(context.Context) error { return nil }
	if err := controller.CloseBenchmarkTarget(context.Background(), reservation, fence); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("quota spec alone established barrier: %v", err)
	}
	quota, err := client.CoreV1().ResourceQuotas(benchmark.Namespace(reservation.RunID)).Get(context.Background(), "benchmark-budget", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quota.Status.Hard = quota.Spec.Hard.DeepCopy()
	if _, err := client.CoreV1().ResourceQuotas(quota.Namespace).UpdateStatus(context.Background(), quota, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := controller.CloseBenchmarkTarget(context.Background(), reservation, fence); err != nil {
		t.Fatal(err)
	}
}

func TestBenchmarkCleanupRetainsQuotaFenceAndProvesAbsenceBeforeRelease(t *testing.T) {
	controller, client, spec, reservation := cleanupFixture(t)
	confirmZeroQuota(t, controller, client, reservation)
	fence := func(context.Context) error { return nil }
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "perfeng-pod-startup-0", Namespace: benchmark.Namespace(reservation.RunID), Labels: map[string]string{runLabel: reservation.RunID, "perfeng.io/benchmark": "pod-startup-contained"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: benchmark.CandidateImage}}, NodeSelector: map[string]string{"workload": "sut"}}}
	if _, err := client.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		clean, receipt, err := controller.CleanupBenchmark(context.Background(), spec, reservation, nil, fence)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 2 && (clean || len(receipt) != 0) {
			t.Fatal("deletion acceptance was treated as absence")
		}
		if attempt == 2 && (!clean || len(receipt) == 0) {
			t.Fatal("clean target did not produce audit")
		}
	}
	quota, err := client.CoreV1().ResourceQuotas(pod.Namespace).Get(context.Background(), "benchmark-budget", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	limit := quota.Spec.Hard[corev1.ResourcePods]
	if limit.Cmp(resource.MustParse("0")) != 0 {
		t.Fatalf("quota fence not retained: %v", err)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), pod.Namespace, metav1.GetOptions{}); err != nil {
		t.Fatal("namespace tombstone removed")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "delete" {
			continue
		}
		options := action.(clienttesting.DeleteAction).GetDeleteOptions()
		if action.GetResource().Resource == "namespaces" || options.Preconditions == nil || options.Preconditions.UID == nil || options.Preconditions.ResourceVersion == nil || options.GracePeriodSeconds != nil {
			t.Fatalf("unsafe cleanup delete: %#v", action)
		}
	}
}

func TestBenchmarkCleanupRejectsForeignPodsAndChangedAuthority(t *testing.T) {
	for _, failure := range []string{"foreign pod", "foreign role", "changed role", "namespace replacement", "cluster replacement", "lease lost", "unknown node", "lost quota barrier", "reopened quota barrier"} {
		t.Run(failure, func(t *testing.T) {
			controller, client, spec, reservation := cleanupFixture(t)
			confirmZeroQuota(t, controller, client, reservation)
			ctx := context.Background()
			name := benchmark.ResourceName(reservation.RunID)
			switch failure {
			case "foreign pod":
				if _, err := client.CoreV1().Pods(benchmark.Namespace(reservation.RunID)).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "foreign role", "changed role":
				role, err := client.RbacV1().Roles(benchmark.Namespace(reservation.RunID)).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if failure == "foreign role" {
					role.Labels[managedByLabel] = "foreign"
				} else {
					role.Rules = append(role.Rules, rbacv1.PolicyRule{Verbs: []string{"get"}, Resources: []string{"secrets"}})
				}
				if _, err := client.RbacV1().Roles(role.Namespace).Update(ctx, role, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "namespace replacement", "cluster replacement":
				ns := benchmark.Namespace(reservation.RunID)
				if failure == "cluster replacement" {
					ns = "kube-system"
				}
				value, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				value.UID = types.UID("00000000-0000-4000-8000-000000000088")
				if _, err := client.CoreV1().Namespaces().Update(ctx, value, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "unknown node":
				node, err := client.CoreV1().Nodes().Get(ctx, "perfeng-local-worker2", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				node.Status.Conditions[0].Status = corev1.ConditionUnknown
				if _, err := client.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "lost quota barrier", "reopened quota barrier":
				reservation.Cleanup.FencedAt = benchmarkTestTime
				quotas := client.CoreV1().ResourceQuotas(benchmark.Namespace(reservation.RunID))
				if failure == "lost quota barrier" {
					if err := quotas.Delete(ctx, "benchmark-budget", metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
				} else {
					quota, err := quotas.Get(ctx, "benchmark-budget", metav1.GetOptions{})
					if err != nil {
						t.Fatal(err)
					}
					quota.Spec.Hard[corev1.ResourcePods] = resource.MustParse("10")
					if _, err := quotas.Update(ctx, quota, metav1.UpdateOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			client.ClearActions()
			fence := func(context.Context) error {
				if failure == "lease lost" {
					return run.ErrLeaseLost
				}
				return nil
			}
			clean, receipt, err := controller.CleanupBenchmark(ctx, spec, reservation, nil, fence)
			if err == nil || clean || len(receipt) != 0 {
				t.Fatalf("unsafe cleanup success: %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "delete" && action.(clienttesting.DeleteAction).GetName() == name && action.GetResource().Resource == "roles" {
					t.Fatal("foreign/changed role deleted")
				}
			}
		})
	}
}

func TestBenchmarkCleanupDoesNotForceWorkloadPods(t *testing.T) {
	controller, client, spec, reservation := cleanupFixture(t)
	confirmZeroQuota(t, controller, client, reservation)
	client.PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "perfeng-pod-startup-1", Namespace: benchmark.Namespace(reservation.RunID), Labels: map[string]string{runLabel: reservation.RunID, "perfeng.io/benchmark": "pod-startup-contained"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: benchmark.CandidateImage}}, NodeSelector: map[string]string{"workload": "sut"}}}
	if _, err := client.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		clean, receipt, err := controller.CleanupBenchmark(context.Background(), spec, reservation, nil, func(context.Context) error { return nil })
		if err != nil || clean || len(receipt) != 0 {
			t.Fatalf("accepted deletion released running Pod: %v", err)
		}
	}
}

func TestBenchmarkCleanupWaitsForExecutorPodsAndRejectsConflictingOwnership(t *testing.T) {
	for _, conflicting := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "conflicting"}[conflicting], func(t *testing.T) {
			controller, client, spec, reservation := cleanupFixture(t)
			confirmZeroQuota(t, controller, client, reservation)
			owner := metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: reservation.RunID, UID: types.UID("00000000-0000-4000-8000-000000000077")}
			controllerReference := true
			owner.Controller = &controllerReference
			execution := Execution{RunID: reservation.RunID, Namespace: "perf-generators", JobName: reservation.RunID, UID: owner.UID, SpecSHA256: strings.Repeat("a", 64)}
			if conflicting {
				owner.UID = types.UID("00000000-0000-4000-8000-000000000078")
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "executor-pod", Namespace: "perf-generators",
				Labels: map[string]string{runLabel: reservation.RunID, managedByLabel: managedByValue}, OwnerReferences: []metav1.OwnerReference{owner}},
				Spec: corev1.PodSpec{ServiceAccountName: benchmark.ResourceName(reservation.RunID)}}
			if _, err := client.CoreV1().Pods(pod.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			clean, receipt, err := controller.CleanupBenchmark(context.Background(), spec, reservation, &execution, func(context.Context) error { return nil })
			if clean || len(receipt) != 0 || (conflicting && !errors.Is(err, ErrJobConflict)) || (!conflicting && err != nil) {
				t.Fatalf("executor presence accepted as stopped: %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "delete" {
					t.Fatal("cleanup removed authority before the executor stopped")
				}
			}
		})
	}
}

func TestBenchmarkCleanupPermitsPressureReliefWithoutFreshAdmission(t *testing.T) {
	controller, client, spec, reservation := cleanupFixture(t)
	confirmZeroQuota(t, controller, client, reservation)
	node, err := client.CoreV1().Nodes().Get(context.Background(), "perfeng-local-worker2", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.Images = nil
	for index := range node.Status.Conditions {
		if node.Status.Conditions[index].Type == corev1.NodeMemoryPressure {
			node.Status.Conditions[index].Status = corev1.ConditionTrue
		}
	}
	if _, err := client.CoreV1().Nodes().UpdateStatus(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fence := func(context.Context) error { return nil }
	if clean, _, err := controller.CleanupBenchmark(context.Background(), spec, reservation, nil, fence); err != nil || clean {
		t.Fatalf("pressure blocked authority removal: %v", err)
	}
	if clean, _, err := controller.CleanupBenchmark(context.Background(), spec, reservation, nil, fence); err != nil || !clean {
		t.Fatalf("pressure blocked verified cleanup: %v", err)
	}
}

func TestBenchmarkCleanupChecksServerRequestTimeout(t *testing.T) {
	for _, argument := range []string{"--request-timeout=0s", "--request-timeout=3m", "--request-timeout=invalid", "--request-timeout"} {
		t.Run(argument, func(t *testing.T) {
			controller, client, _, reservation := cleanupFixture(t)
			pod, err := client.CoreV1().Pods("kube-system").Get(context.Background(), "kube-apiserver-perfeng-local-control-plane", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			pod.Spec.Containers[0].Args = []string{argument}
			if _, err := client.CoreV1().Pods("kube-system").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			if err := controller.CloseBenchmarkTarget(context.Background(), reservation, func(context.Context) error { return nil }); !errors.Is(err, run.ErrBenchmarkConflict) {
				t.Fatalf("unbounded API accepted: %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "update" || action.GetVerb() == "create" || action.GetVerb() == "delete" {
					t.Fatal("unbounded server allowed write")
				}
			}
		})
	}
}
