package kubernetes

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const benchmarkIntentAnnotation = "perfeng.io/benchmark-plan-sha256"

// BenchmarkFence rechecks the current lease, Run revision/state and retained
// reservation immediately before an external write. It must not hold a database
// transaction across the API call. A racing cancellation is recovered, not undone.
type BenchmarkFence func(context.Context) error

// EnsureNamespace creates or adopts only the exact Run-owned namespace after a
// durable reservation exists. It never substitutes a new incarnation for a bound UID.
func (controller *BenchmarkController) EnsureNamespace(ctx context.Context, reservation run.BenchmarkReservation, fence BenchmarkFence) (string, error) {
	if !contract.ValidID(reservation.RunID) || !reservation.Intent.Valid() || fence == nil || len(benchmark.Namespace(reservation.RunID)) > 63 {
		return "", run.ErrValidation
	}
	if reservation.NamespaceUID != "" {
		if err := fence(ctx); err != nil {
			return "", err
		}
		return reservation.NamespaceUID, controller.CheckNamespace(ctx, reservation)
	}
	if err := controller.CheckCluster(ctx, reservation.Intent.ClusterUID); err != nil {
		return "", err
	}
	desired := benchmarkNamespace(reservation)
	namespace, err := ensureBenchmarkObject(ctx, controller.client.CoreV1().Namespaces(), desired, fence, func(actual *corev1.Namespace) bool {
		if actual.DeletionTimestamp != nil || !benchmarkMetadataMatches(desired, actual) ||
			!run.ValidBenchmarkUID(string(actual.UID)) || (reservation.NamespaceUID != "" && reservation.NamespaceUID != string(actual.UID)) {
			return false
		}
		return true
	})
	if err != nil {
		return "", err
	}
	return string(namespace.UID), nil
}

// CheckNamespace verifies ownership and incarnation without creating or repairing anything.
func (controller *BenchmarkController) CheckNamespace(ctx context.Context, reservation run.BenchmarkReservation) error {
	if !run.ValidBenchmarkUID(reservation.NamespaceUID) {
		return run.ErrValidation
	}
	if err := controller.CheckCluster(ctx, reservation.Intent.ClusterUID); err != nil {
		return err
	}
	namespace, err := controller.client.CoreV1().Namespaces().Get(ctx, benchmark.Namespace(reservation.RunID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return run.ErrBenchmarkConflict
	}
	if err != nil {
		return classifyAPIError("benchmark namespace identity", err)
	}
	if string(namespace.UID) != reservation.NamespaceUID || namespace.DeletionTimestamp != nil ||
		!benchmarkMetadataMatches(benchmarkNamespace(reservation), namespace) {
		return run.ErrBenchmarkConflict
	}
	return nil
}

// EnsureResources creates/adopts immutable inputs and least-privilege per-Run
// authority. Every effect rechecks storage fencing and namespace incarnation.
// Existing objects are compared, never patched, deleted or replaced.
func (controller *BenchmarkController) EnsureResources(ctx context.Context, spec benchmark.Spec, reservation run.BenchmarkReservation, fence BenchmarkFence) error {
	expected, err := benchmark.RequestBytes(spec, reservation.RunID, reservation.NamespaceUID)
	if err != nil || string(expected) != string(reservation.Request) || fence == nil ||
		spec.ClusterID != reservation.Intent.TargetID || spec.ClusterUID != reservation.Intent.ClusterUID {
		return run.ErrBenchmarkConflict
	}
	guard := func(ctx context.Context) error {
		if err := fence(ctx); err != nil {
			return err
		}
		return controller.CheckNamespace(ctx, reservation)
	}
	if err := guard(ctx); err != nil {
		return err
	}
	// A partial retry may already have authority, but a fresh executor must not
	// silently add ten more Pods to an occupied target namespace.
	pods, err := controller.client.CoreV1().Pods(benchmark.Namespace(reservation.RunID)).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return classifyAPIError("benchmark target inventory", err)
	}
	if len(pods.Items) != 0 || pods.Continue != "" {
		return run.ErrBenchmarkConflict
	}
	name := benchmark.ResourceName(reservation.RunID)
	target := benchmark.Namespace(reservation.RunID)
	meta := func(namespace string) metav1.ObjectMeta { return benchmarkMetadata(name, namespace, reservation) }
	quota := &corev1.ResourceQuota{ObjectMeta: benchmarkMetadata("benchmark-budget", target, reservation), Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods: resource.MustParse("10"), corev1.ResourceLimitsCPU: resource.MustParse("100m"),
		corev1.ResourceLimitsMemory: resource.MustParse("160Mi"), corev1.ResourceLimitsEphemeralStorage: resource.MustParse("10Mi"),
	}}}
	if _, err := ensureBenchmarkObject(ctx, controller.client.CoreV1().ResourceQuotas(target), quota, guard, func(actual *corev1.ResourceQuota) bool {
		return benchmarkMetadataMatches(quota, actual) && apiequality.Semantic.DeepEqual(quota.Spec, actual.Spec)
	}); err != nil {
		return err
	}
	sa := &corev1.ServiceAccount{ObjectMeta: meta(controller.namespace), AutomountServiceAccountToken: ptr.To(false)}
	if _, err := ensureBenchmarkObject(ctx, controller.client.CoreV1().ServiceAccounts(controller.namespace), sa, guard, func(actual *corev1.ServiceAccount) bool {
		return benchmarkMetadataMatches(sa, actual) && actual.AutomountServiceAccountToken != nil && !*actual.AutomountServiceAccountToken && len(actual.Secrets) == 0 && len(actual.ImagePullSecrets) == 0
	}); err != nil {
		return err
	}
	role := &rbacv1.Role{ObjectMeta: meta(target), Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"resourcequotas"}, ResourceNames: []string{"benchmark-budget"}, Verbs: []string{"get"}},
	}}
	if _, err := ensureBenchmarkObject(ctx, controller.client.RbacV1().Roles(target), role, guard, func(actual *rbacv1.Role) bool {
		return benchmarkMetadataMatches(role, actual) && reflect.DeepEqual(role.Rules, actual.Rules)
	}); err != nil {
		return err
	}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: meta(""), Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "events"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{"metrics.k8s.io"}, Resources: []string{"nodes"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{""}, Resources: []string{"namespaces"}, ResourceNames: []string{"kube-system", target}, Verbs: []string{"get"}},
		{NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/version"}, Verbs: []string{"get"}},
	}}
	if _, err := ensureBenchmarkObject(ctx, controller.client.RbacV1().ClusterRoles(), clusterRole, guard, func(actual *rbacv1.ClusterRole) bool {
		return benchmarkMetadataMatches(clusterRole, actual) && actual.AggregationRule == nil && reflect.DeepEqual(clusterRole.Rules, actual.Rules)
	}); err != nil {
		return err
	}
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: controller.namespace}}
	binding := &rbacv1.RoleBinding{ObjectMeta: meta(target), Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}}
	if _, err := ensureBenchmarkObject(ctx, controller.client.RbacV1().RoleBindings(target), binding, guard, func(actual *rbacv1.RoleBinding) bool {
		return benchmarkMetadataMatches(binding, actual) && reflect.DeepEqual(binding.Subjects, actual.Subjects) && binding.RoleRef == actual.RoleRef
	}); err != nil {
		return err
	}
	clusterBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: meta(""), Subjects: subjects, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name}}
	if _, err := ensureBenchmarkObject(ctx, controller.client.RbacV1().ClusterRoleBindings(), clusterBinding, guard, func(actual *rbacv1.ClusterRoleBinding) bool {
		return benchmarkMetadataMatches(clusterBinding, actual) && reflect.DeepEqual(clusterBinding.Subjects, actual.Subjects) && clusterBinding.RoleRef == actual.RoleRef
	}); err != nil {
		return err
	}
	inputs := &corev1.ConfigMap{ObjectMeta: meta(controller.namespace), Immutable: ptr.To(true), Data: map[string]string{
		"request.json": string(reservation.Request), "environment.json": string(spec.EnvironmentBytes),
	}}
	_, err = ensureBenchmarkObject(ctx, controller.client.CoreV1().ConfigMaps(controller.namespace), inputs, guard, func(actual *corev1.ConfigMap) bool {
		return benchmarkMetadataMatches(inputs, actual) && actual.Immutable != nil && *actual.Immutable && reflect.DeepEqual(inputs.Data, actual.Data) && len(actual.BinaryData) == 0
	})
	return err
}

type benchmarkObject interface {
	metav1.Object
	runtime.Object
}
type benchmarkObjects[T benchmarkObject] interface {
	Get(context.Context, string, metav1.GetOptions) (T, error)
	Create(context.Context, T, metav1.CreateOptions) (T, error)
}

// ensureBenchmarkObject is create-only: it never repairs mismatching authority.
func ensureBenchmarkObject[T benchmarkObject](ctx context.Context, client benchmarkObjects[T], desired T, fence BenchmarkFence, matches func(T) bool) (T, error) {
	var zero T
	if err := fence(ctx); err != nil {
		return zero, err
	}
	actual, err := client.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err := fence(ctx); err != nil {
			return zero, err
		}
		actual, err = client.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			actual, err = client.Get(ctx, desired.GetName(), metav1.GetOptions{})
		}
	}
	if err != nil {
		return zero, classifyAPIError("benchmark resource create/adopt", err)
	}
	if !matches(actual) {
		return zero, run.ErrBenchmarkConflict
	}
	return actual, nil
}

func benchmarkMetadata(name, namespace string, reservation run.BenchmarkReservation) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: withIdentity(nil, reservation.RunID), Annotations: map[string]string{benchmarkIntentAnnotation: reservation.Intent.PlanSHA256}}
}

func benchmarkMetadataMatches(desired, actual metav1.Object) bool {
	labels := desired.GetLabels()
	if _, namespace := desired.(*corev1.Namespace); namespace && actual.GetLabels()[corev1.LabelMetadataName] == desired.GetName() {
		labels = withValue(labels, corev1.LabelMetadataName, desired.GetName())
	}
	return actual.GetName() == desired.GetName() && actual.GetNamespace() == desired.GetNamespace() && actual.GetDeletionTimestamp() == nil &&
		len(actual.GetOwnerReferences()) == 0 && reflect.DeepEqual(labels, actual.GetLabels()) && reflect.DeepEqual(desired.GetAnnotations(), actual.GetAnnotations())
}

func benchmarkNamespace(reservation run.BenchmarkReservation) *corev1.Namespace {
	meta := benchmarkMetadata(benchmark.Namespace(reservation.RunID), "", reservation)
	meta.Labels["perfeng.io/owner"] = managedByValue
	meta.Labels["pod-security.kubernetes.io/enforce"] = "restricted"
	meta.Labels["pod-security.kubernetes.io/enforce-version"] = "v1.35"
	return &corev1.Namespace{ObjectMeta: meta}
}
