package kubernetes

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const benchmarkTimestamp = "2006-01-02T15:04:05.000000Z"

// BenchmarkMetrics reads bounded node usage from the existing metrics provider.
// Missing, denied, stale or malformed observations must remain errors, never zeros.
type BenchmarkMetrics func(context.Context) ([]byte, error)

// BenchmarkController admits and materializes the single reviewed integration
// cohort. It never installs metrics, pulls candidate images, replaces resources
// or releases reservations. Storage authority is supplied separately per effect.
type BenchmarkController struct {
	client    clientset.Interface
	metrics   BenchmarkMetrics
	namespace string
	now       func() time.Time
}

// NewBenchmarkController binds the API to the reviewed executor namespace without I/O.
func NewBenchmarkController(client clientset.Interface, metrics BenchmarkMetrics, namespace string, now func() time.Time) (*BenchmarkController, error) {
	if client == nil || metrics == nil || namespace != "perf-generators" || now == nil {
		return nil, run.ErrValidation
	}
	return &BenchmarkController{client: client, metrics: metrics, namespace: namespace, now: now}, nil
}

// Qualify reobserves actual target identity, placement, conditions, capacity,
// usage and interference immediately before dispatch. It returns only accepted
// admission bytes; a missing observation grants no write authority. The selected
// descriptor attests the operator-qualified node image; runtime inventory must
// reproduce its exact fingerprint. This does not qualify production performance.
func (controller *BenchmarkController) Qualify(ctx context.Context, spec benchmark.Spec) ([]byte, error) {
	if spec.Validate() != nil {
		return nil, run.ErrValidation
	}
	started := controller.now().UTC()
	if err := controller.CheckCluster(ctx, spec.ClusterUID); err != nil {
		return nil, err
	}
	nodes, err := controller.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 256})
	if err != nil {
		return nil, classifyAPIError("benchmark node inventory", err)
	}
	if nodes.Continue != "" || len(nodes.Items) != 3 {
		return nil, benchmark.ErrNotQualified
	}
	inventory, err := benchmarkInventory(nodes.Items)
	if err != nil {
		return nil, err
	}
	basis := map[string]any{"profileSha256": benchmark.ProfileSHA256, "nodeImage": benchmark.NodeImage, "kubeSystemUid": spec.ClusterUID, "observedNodes": inventory}
	encoded, err := json.Marshal(basis)
	if err != nil || run.BenchmarkDigest(encoded) != spec.Environment.Fingerprint {
		return nil, benchmark.ErrNotQualified
	}

	metricBytes, err := controller.metrics(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, benchmark.ErrNotQualified
	}
	samples, windowStart, err := benchmarkUsage(metricBytes, nodes.Items, controller.now().UTC())
	if err != nil {
		return nil, err
	}
	if windowStart.Before(started) {
		started = windowStart
	}
	pods, err := controller.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 4096})
	if err != nil {
		return nil, classifyAPIError("benchmark interference inventory", err)
	}
	if pods.Continue != "" || len(pods.Items) > 4096 || !benchmarkQuiet(pods.Items) {
		return nil, benchmark.ErrNotQualified
	}
	if err := controller.CheckCluster(ctx, spec.ClusterUID); err != nil {
		return nil, err
	}
	// Recheck the inventory after metrics and Pods so a node replacement cannot
	// leave an accepted receipt describing an earlier incarnation.
	current, err := controller.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 256})
	if err != nil {
		return nil, classifyAPIError("benchmark inventory recheck", err)
	}
	verified, err := benchmarkInventory(current.Items)
	if err != nil || current.Continue != "" {
		return nil, benchmark.ErrNotQualified
	}
	basis["observedNodes"] = verified
	verifiedBytes, err := json.Marshal(basis)
	if err != nil || run.BenchmarkDigest(verifiedBytes) != spec.Environment.Fingerprint {
		return nil, benchmark.ErrNotQualified
	}
	finished := controller.now().UTC()
	if finished.Before(started) || finished.Sub(started) > 3*time.Minute {
		return nil, benchmark.ErrNotQualified
	}
	checks := make([]map[string]string, 0, 6)
	for _, id := range []string{"cluster-identity", "node-readiness", "placement", "capacity", "resource-pressure", "interference"} {
		checks = append(checks, map[string]string{"id": id, "status": "PASS", "reason": "Fresh API observations satisfy the selected integration profile."})
	}
	return json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": "KubernetesBenchmarkEvidence", "scope": map[string]string{"kind": "preflight"},
		"profile":     map[string]string{"id": "local-kind-integration", "version": "1.0.0", "sha256": benchmark.ProfileSHA256, "purpose": "integration"},
		"environment": spec.Environment, "target": map[string]string{"clusterId": spec.ClusterID, "kubeSystemUid": spec.ClusterUID},
		"captureWindow": map[string]string{"start": started.Format(benchmarkTimestamp), "end": finished.Format(benchmarkTimestamp)},
		"status":        "ACCEPTED", "checks": checks, "nodes": inventory, "samples": samples,
	})
}

// CheckCluster fails closed if the API endpoint no longer represents the selected incarnation.
func (controller *BenchmarkController) CheckCluster(ctx context.Context, uid string) error {
	if !run.ValidBenchmarkUID(uid) {
		return run.ErrValidation
	}
	namespace, err := controller.client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return classifyAPIError("benchmark cluster identity", err)
	}
	if string(namespace.UID) != uid || namespace.DeletionTimestamp != nil {
		return run.ErrBenchmarkConflict
	}
	return nil
}

func benchmarkInventory(nodes []corev1.Node) ([]map[string]any, error) {
	return benchmarkNodeInventory(nodes, true)
}

func benchmarkNodeInventory(nodes []corev1.Node, admission bool) ([]map[string]any, error) {
	roles := map[string]string{"perfeng-local-control-plane": "control-plane", "perfeng-local-worker": "performance-generator", "perfeng-local-worker2": "sut"}
	if len(nodes) != len(roles) {
		return nil, benchmark.ErrNotQualified
	}
	seen := map[string]bool{}
	inventory := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		role, approved := roles[node.Name]
		cpu, memory := node.Status.Allocatable.Cpu(), node.Status.Allocatable.Memory()
		info := node.Status.NodeInfo
		if !approved || seen[node.Name] || seen[string(node.UID)] || !run.ValidBenchmarkUID(string(node.UID)) ||
			node.DeletionTimestamp != nil || node.Spec.Unschedulable || node.Labels["workload"] != role ||
			node.Labels["kubernetes.io/os"] != "linux" || node.Labels["kubernetes.io/arch"] != "amd64" ||
			info.KubeletVersion != "v1.35.0" || info.OperatingSystem != "linux" || info.Architecture != "amd64" || info.ContainerRuntimeVersion == "" ||
			cpu.MilliValue() < 500 || memory.Value() < 1<<30 || cpu.MilliValue() > 1<<53-1 || memory.Value() > 1<<53-1 {
			return nil, benchmark.ErrNotQualified
		}
		seen[node.Name], seen[string(node.UID)] = true, true
		conditions := map[corev1.NodeConditionType]corev1.ConditionStatus{}
		for _, condition := range node.Status.Conditions {
			if _, duplicate := conditions[condition.Type]; duplicate {
				return nil, benchmark.ErrNotQualified
			}
			conditions[condition.Type] = condition.Status
		}
		if conditions[corev1.NodeReady] != corev1.ConditionTrue || (admission && (conditions[corev1.NodeMemoryPressure] != corev1.ConditionFalse ||
			conditions[corev1.NodeDiskPressure] != corev1.ConditionFalse || conditions[corev1.NodePIDPressure] != corev1.ConditionFalse)) {
			return nil, benchmark.ErrNotQualified
		}
		if admission && role == "sut" {
			warm := false
			for _, image := range node.Status.Images {
				for _, name := range image.Names {
					if name == benchmark.CandidateImage {
						warm = true
					}
				}
			}
			if !warm {
				return nil, benchmark.ErrNotQualified
			}
		}
		inventory = append(inventory, map[string]any{
			"name": node.Name, "uid": string(node.UID), "kubernetesVersion": info.KubeletVersion,
			"containerRuntimeVersion": info.ContainerRuntimeVersion, "architecture": info.Architecture,
			"allocatable": map[string]any{"cpuMilliCores": cpu.MilliValue(), "memoryBytes": memory.Value()},
		})
	}
	sort.Slice(inventory, func(i, j int) bool { return inventory[i]["name"].(string) < inventory[j]["name"].(string) })
	return inventory, nil
}

func benchmarkUsage(data []byte, nodes []corev1.Node, now time.Time) ([]map[string]any, time.Time, error) {
	var list struct {
		APIVersion string          `json:"apiVersion"`
		Kind       string          `json:"kind"`
		Metadata   json.RawMessage `json:"metadata"`
		Items      []struct {
			Metadata  metav1.ObjectMeta   `json:"metadata"`
			Timestamp metav1.Time         `json:"timestamp"`
			Window    metav1.Duration     `json:"window"`
			Usage     corev1.ResourceList `json:"usage"`
		} `json:"items"`
	}
	if !jsondocument.Valid(data, 1<<20) || !jsondocument.Decode(data, &list) || list.APIVersion != "metrics.k8s.io/v1beta1" || list.Kind != "NodeMetricsList" || len(list.Items) != len(nodes) {
		return nil, time.Time{}, benchmark.ErrNotQualified
	}
	byName := map[string]corev1.Node{}
	for _, node := range nodes {
		byName[node.Name] = node
	}
	samples := make([]map[string]any, 0, len(nodes))
	start := now
	for _, item := range list.Items {
		node, exists := byName[item.Metadata.Name]
		age := now.Sub(item.Timestamp.Time)
		cpu, cpuPresent := item.Usage[corev1.ResourceCPU]
		memory, memoryPresent := item.Usage[corev1.ResourceMemory]
		if !exists || !cpuPresent || !memoryPresent || age < 0 || age > 120*time.Second || item.Window.Duration <= 0 || item.Window.Duration > 60*time.Second ||
			cpu.Cmp(*node.Status.Allocatable.Cpu()) > 0 || memory.Cmp(*node.Status.Allocatable.Memory()) > 0 ||
			cpu.Sign() < 0 || memory.Sign() < 0 || cpu.MilliValue() > node.Status.Allocatable.Cpu().MilliValue()*3/4 || memory.Value() > node.Status.Allocatable.Memory().Value()*85/100 {
			return nil, time.Time{}, benchmark.ErrNotQualified
		}
		delete(byName, item.Metadata.Name)
		windowStart := item.Timestamp.Add(-item.Window.Duration)
		if windowStart.Before(start) {
			start = windowStart
		}
		samples = append(samples, map[string]any{
			"nodeUid": string(node.UID), "observedAt": now.Format(benchmarkTimestamp),
			"conditions": map[string]string{"ready": "TRUE", "memoryPressure": "FALSE", "diskPressure": "FALSE", "pidPressure": "FALSE"},
			"usage":      map[string]any{"source": "metrics-api", "windowStart": windowStart.UTC().Format(benchmarkTimestamp), "windowEnd": item.Timestamp.Time.UTC().Format(benchmarkTimestamp), "cpuMilliCores": cpu.MilliValue(), "memoryBytes": memory.Value()},
		})
	}
	return samples, start, nil
}

func benchmarkQuiet(pods []corev1.Pod) bool {
	allowed := map[string]bool{"kube-system": true, "local-path-storage": true, "argocd": true, "perf-platform": true, "perf-sut": true, "monitoring": true}
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed && !allowed[pod.Namespace] {
			return false
		}
	}
	return true
}
