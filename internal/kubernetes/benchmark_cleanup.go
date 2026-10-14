package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// InspectCleanupNamespace discovers a possibly ambiguous create without replacing
// or creating anything. A missing bound namespace or changed incarnation conflicts.
func (controller *BenchmarkController) InspectCleanupNamespace(ctx context.Context, reservation run.BenchmarkReservation) (string, bool, error) {
	if !contract.ValidID(reservation.RunID) || !reservation.Intent.Valid() || reservation.Cleanup == nil || !reservation.Cleanup.ReleasedAt.IsZero() {
		return "", false, run.ErrValidation
	}
	if err := controller.CheckCluster(ctx, reservation.Intent.ClusterUID); err != nil {
		return "", false, err
	}
	actual, err := controller.client.CoreV1().Namespaces().Get(ctx, benchmark.Namespace(reservation.RunID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if reservation.NamespaceUID != "" || len(reservation.Request) != 0 {
			return "", false, run.ErrBenchmarkConflict
		}
		return "", false, nil
	}
	if err != nil {
		return "", false, classifyAPIError("benchmark cleanup namespace", err)
	}
	if !benchmarkMetadataMatches(benchmarkNamespace(reservation), actual) || !run.ValidBenchmarkUID(string(actual.UID)) ||
		(reservation.NamespaceUID != "" && reservation.NamespaceUID != string(actual.UID)) {
		return "", false, run.ErrBenchmarkConflict
	}
	return string(actual.UID), true, nil
}

// CloseBenchmarkTarget creates or tightens only the owned safety quota to zero
// Pods. This tombstone is retained after cleanup, never restored or pruned here.
func (controller *BenchmarkController) CloseBenchmarkTarget(ctx context.Context, reservation run.BenchmarkReservation, fence BenchmarkFence) error {
	if fence == nil {
		return run.ErrValidation
	}
	if err := controller.benchmarkRequestBound(ctx); err != nil {
		return err
	}
	if err := fence(ctx); err != nil {
		return err
	}
	if _, exists, err := controller.InspectCleanupNamespace(ctx, reservation); err != nil {
		return err
	} else if !exists {
		return run.ErrBenchmarkConflict
	}
	desired := benchmarkTemplates(controller.namespace, benchmark.Spec{}, reservation).quota
	desired.Spec.Hard[corev1.ResourcePods] = resource.MustParse("0")
	client := controller.client.CoreV1().ResourceQuotas(desired.Namespace)
	actual, err := client.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if !reservation.Cleanup.FencedAt.IsZero() {
			return run.ErrBenchmarkConflict
		}
		if err := fence(ctx); err != nil {
			return err
		}
		if err := controller.CheckNamespace(ctx, reservation); err != nil {
			return err
		}
		actual, err = client.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return run.ErrUnavailable
		}
	}
	if err != nil {
		return classifyAPIError("benchmark cleanup quota", err)
	}
	if !benchmarkMetadataMatches(desired, actual) || actual.UID == "" || actual.ResourceVersion == "" {
		return run.ErrBenchmarkConflict
	}
	if apiequality.Semantic.DeepEqual(desired.Spec, actual.Spec) {
		limit, present := actual.Status.Hard[corev1.ResourcePods]
		if !present || !limit.IsZero() {
			return run.ErrUnavailable
		}
		return nil
	}
	if !reservation.Cleanup.FencedAt.IsZero() {
		return run.ErrBenchmarkConflict
	}
	open := desired.DeepCopy()
	open.Spec.Hard[corev1.ResourcePods] = resource.MustParse("10")
	if !apiequality.Semantic.DeepEqual(open.Spec, actual.Spec) {
		return run.ErrBenchmarkConflict
	}
	if err := fence(ctx); err != nil {
		return err
	}
	if err := controller.CheckNamespace(ctx, reservation); err != nil {
		return err
	}
	closed := actual.DeepCopy()
	closed.Spec = desired.Spec
	if _, err := client.Update(ctx, closed, metav1.UpdateOptions{}); err != nil {
		return classifyAPIError("benchmark close quota", err)
	}
	return run.ErrUnavailable
}

// CleanupBenchmark verifies the permanent quota barrier, waits for the exact
// executor and deletes only scoped, owned Pods/authority with UID/RV preconditions.
// It never deletes a namespace, force-deletes a Pod or treats deletion acceptance
// as proof of termination. Receipt bytes contain identities, never credentials.
func (controller *BenchmarkController) CleanupBenchmark(ctx context.Context, spec benchmark.Spec, reservation run.BenchmarkReservation, execution *Execution, fence BenchmarkFence) (bool, []byte, error) {
	if spec.Validate() != nil || reservation.Cleanup == nil || !reservation.Cleanup.Ready || fence == nil {
		return false, nil, run.ErrValidation
	}
	if spec.ClusterUID != reservation.Intent.ClusterUID || spec.ClusterID != reservation.Intent.TargetID {
		return false, nil, run.ErrBenchmarkConflict
	}
	if execution != nil && (!execution.Valid() || execution.RunID != reservation.RunID || execution.Namespace != controller.namespace) {
		return false, nil, ErrJobConflict
	}
	if err := fence(ctx); err != nil {
		return false, nil, err
	}
	_, exists, err := controller.InspectCleanupNamespace(ctx, reservation)
	if err != nil {
		return false, nil, err
	}
	if exists {
		if err := controller.CloseBenchmarkTarget(ctx, reservation, fence); err != nil {
			return false, nil, err
		}
	}
	stopped, err := controller.benchmarkExecutorStopped(ctx, reservation, execution)
	if err != nil || !stopped {
		return false, nil, err
	}
	// A disconnected kubelet must not be converted into successful cleanup by
	// removing API objects. The reviewed inventory also binds node incarnation.
	nodes, err := controller.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 256})
	if err != nil {
		return false, nil, classifyAPIError("benchmark cleanup nodes", err)
	}
	inventory, err := benchmarkNodeInventory(nodes.Items, false)
	if err != nil || nodes.Continue != "" {
		return false, nil, benchmark.ErrNotQualified
	}
	basis, err := json.Marshal(map[string]any{"profileSha256": benchmark.ProfileSHA256, "nodeImage": benchmark.NodeImage, "kubeSystemUid": spec.ClusterUID, "observedNodes": inventory})
	if err != nil || run.BenchmarkDigest(basis) != spec.Environment.Fingerprint {
		return false, nil, run.ErrBenchmarkConflict
	}
	if exists {
		clean, err := controller.cleanupBenchmarkPods(ctx, reservation, fence)
		if err != nil || !clean {
			return false, nil, err
		}
	}
	objects := benchmarkTemplates(controller.namespace, spec, reservation)
	guard := func(ctx context.Context) error {
		if err := fence(ctx); err != nil {
			return err
		}
		_, _, err := controller.InspectCleanupNamespace(ctx, reservation)
		return err
	}
	clean := true
	remove := func(absent bool, err error) error { clean = clean && absent; return err }
	if err := remove(deleteBenchmarkObject(ctx, controller.client.RbacV1().RoleBindings(objects.binding.Namespace), objects.binding, guard)); err != nil {
		return false, nil, err
	}
	if err := remove(deleteBenchmarkObject(ctx, controller.client.RbacV1().ClusterRoleBindings(), objects.clusterBinding, guard)); err != nil {
		return false, nil, err
	}
	if err := remove(deleteBenchmarkObject(ctx, controller.client.RbacV1().Roles(objects.role.Namespace), objects.role, guard)); err != nil {
		return false, nil, err
	}
	if err := remove(deleteBenchmarkObject(ctx, controller.client.RbacV1().ClusterRoles(), objects.clusterRole, guard)); err != nil {
		return false, nil, err
	}
	if err := remove(deleteBenchmarkObject(ctx, controller.client.CoreV1().ServiceAccounts(controller.namespace), objects.account, guard)); err != nil {
		return false, nil, err
	}
	if err := remove(deleteBenchmarkObject(ctx, controller.client.CoreV1().ConfigMaps(controller.namespace), objects.inputs, guard)); err != nil {
		return false, nil, err
	}
	if !clean {
		return false, nil, nil
	}
	if err := guard(ctx); err != nil {
		return false, nil, err
	}
	if stopped, err := controller.benchmarkExecutorStopped(ctx, reservation, execution); err != nil || !stopped {
		return false, nil, err
	}
	if exists {
		if clean, err := controller.cleanupBenchmarkPods(ctx, reservation, fence); err != nil || !clean {
			return false, nil, err
		}
	}
	receipt, err := json.Marshal(run.BenchmarkCleanupProof{
		SchemaVersion: 1, RunID: reservation.RunID, PlanSHA256: reservation.Intent.PlanSHA256,
		ClusterUID: reservation.Intent.ClusterUID, NamespaceUID: reservation.NamespaceUID,
		ExecutorStopped: true, WorkloadStopped: true, AuthorityRemoved: true, NamespaceFenced: true,
	})
	return err == nil, receipt, err
}

func (controller *BenchmarkController) benchmarkExecutorStopped(ctx context.Context, reservation run.BenchmarkReservation, execution *Execution) (bool, error) {
	job, err := controller.client.BatchV1().Jobs(controller.namespace).Get(ctx, reservation.RunID, metav1.GetOptions{})
	if err == nil {
		if execution == nil || matchingExecution(job, *execution) != nil {
			return false, ErrJobConflict
		}
		return false, nil
	}
	if !apierrors.IsNotFound(err) {
		return false, classifyAPIError("benchmark cleanup executor", err)
	}
	pods, err := controller.client.CoreV1().Pods(controller.namespace).List(ctx, metav1.ListOptions{Limit: 4096})
	if err != nil {
		return false, classifyAPIError("benchmark cleanup executor Pods", err)
	}
	if pods.Continue != "" {
		return false, run.ErrUnavailable
	}
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if pod.Labels[runLabel] != reservation.RunID && pod.Spec.ServiceAccountName != benchmark.ResourceName(reservation.RunID) &&
			(owner == nil || owner.Kind != "Job" || owner.Name != reservation.RunID) {
			continue
		}
		if execution == nil || owner == nil || owner.APIVersion != "batch/v1" || owner.Kind != "Job" || owner.Name != execution.JobName || owner.UID != execution.UID ||
			pod.Labels[runLabel] != reservation.RunID || pod.Labels[managedByLabel] != managedByValue {
			return false, ErrJobConflict
		}
		return false, nil
	}
	return true, nil
}

func (controller *BenchmarkController) cleanupBenchmarkPods(ctx context.Context, reservation run.BenchmarkReservation, fence BenchmarkFence) (bool, error) {
	client := controller.client.CoreV1().Pods(benchmark.Namespace(reservation.RunID))
	pods, err := client.List(ctx, metav1.ListOptions{Limit: 64})
	if err != nil {
		return false, classifyAPIError("benchmark cleanup workload", err)
	}
	if pods.Continue != "" || len(pods.Items) > 10 {
		return false, run.ErrBenchmarkConflict
	}
	for _, pod := range pods.Items {
		name := false
		for iteration := 0; iteration < 10; iteration++ {
			name = name || pod.Name == fmt.Sprintf("perfeng-pod-startup-%d", iteration)
		}
		if !name || pod.Namespace != benchmark.Namespace(reservation.RunID) || pod.UID == "" || pod.ResourceVersion == "" ||
			pod.Labels[runLabel] != reservation.RunID || pod.Labels["perfeng.io/benchmark"] != "pod-startup-contained" || len(pod.OwnerReferences) != 0 ||
			len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.EphemeralContainers) != 0 ||
			pod.Spec.Containers[0].Image != benchmark.CandidateImage || pod.Spec.NodeSelector["workload"] != "sut" {
			return false, run.ErrBenchmarkConflict
		}
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if err := fence(ctx); err != nil {
			return false, err
		}
		if err := controller.CheckNamespace(ctx, reservation); err != nil {
			return false, err
		}
		uid, version := pod.UID, pod.ResourceVersion
		if err := client.Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
			return false, classifyAPIError("benchmark delete workload", err)
		}
	}
	return len(pods.Items) == 0, nil
}

type benchmarkDeleter[T benchmarkObject] interface {
	Get(context.Context, string, metav1.GetOptions) (T, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
}

func deleteBenchmarkObject[T benchmarkObject](ctx context.Context, client benchmarkDeleter[T], desired T, fence BenchmarkFence) (bool, error) {
	if err := fence(ctx); err != nil {
		return false, err
	}
	actual, err := client.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, classifyAPIError("benchmark cleanup authority", err)
	}
	copy := actual.DeepCopyObject().(metav1.Object)
	copy.SetDeletionTimestamp(nil)
	if !benchmarkMetadataMatches(desired, copy) || actual.GetUID() == "" || actual.GetResourceVersion() == "" || !benchmarkCleanupBodyMatches(desired, actual) {
		return false, run.ErrBenchmarkConflict
	}
	if actual.GetDeletionTimestamp() != nil {
		return false, nil
	}
	if err := fence(ctx); err != nil {
		return false, err
	}
	uid, version := actual.GetUID(), actual.GetResourceVersion()
	if err := client.Delete(ctx, actual.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil && !apierrors.IsNotFound(err) {
		return false, classifyAPIError("benchmark revoke authority", err)
	}
	return false, nil
}

func benchmarkCleanupBodyMatches(desired, actual runtime.Object) bool {
	switch expected := desired.(type) {
	case *corev1.ServiceAccount:
		value, ok := actual.(*corev1.ServiceAccount)
		return ok && value.AutomountServiceAccountToken != nil && !*value.AutomountServiceAccountToken && len(value.Secrets) == 0 && len(value.ImagePullSecrets) == 0
	case *corev1.ConfigMap:
		value, ok := actual.(*corev1.ConfigMap)
		return ok && value.Immutable != nil && *value.Immutable && reflect.DeepEqual(expected.Data, value.Data) && len(value.BinaryData) == 0
	case *rbacv1.Role:
		value, ok := actual.(*rbacv1.Role)
		return ok && reflect.DeepEqual(expected.Rules, value.Rules)
	case *rbacv1.ClusterRole:
		value, ok := actual.(*rbacv1.ClusterRole)
		return ok && value.AggregationRule == nil && reflect.DeepEqual(expected.Rules, value.Rules)
	case *rbacv1.RoleBinding:
		value, ok := actual.(*rbacv1.RoleBinding)
		return ok && expected.RoleRef == value.RoleRef && reflect.DeepEqual(expected.Subjects, value.Subjects)
	case *rbacv1.ClusterRoleBinding:
		value, ok := actual.(*rbacv1.ClusterRoleBinding)
		return ok && expected.RoleRef == value.RoleRef && reflect.DeepEqual(expected.Subjects, value.Subjects)
	default:
		return false
	}
}
