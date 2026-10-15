package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var benchmarkTestTime = time.Date(2026, 10, 14, 10, 0, 0, 123456000, time.UTC)

func benchmarkFixture(t *testing.T) (*BenchmarkController, *fake.Clientset, benchmark.Spec, []byte) {
	t.Helper()
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "00000000-0000-4000-8000-000000000001"}}}
	objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver-perfeng-local-control-plane", Namespace: "kube-system", Labels: map[string]string{"component": "kube-apiserver"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kube-apiserver", Command: []string{"kube-apiserver"}}}}})
	nodes := make([]corev1.Node, 0, 3)
	metrics := make([]map[string]any, 0, 3)
	for index, name := range []string{"perfeng-local-control-plane", "perfeng-local-worker", "perfeng-local-worker2"} {
		role := []string{"control-plane", "performance-generator", "sut"}[index]
		node := corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+10)), Labels: map[string]string{"workload": role, "kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"}},
			Status: corev1.NodeStatus{
				NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.35.0", OperatingSystem: "linux", Architecture: "amd64", ContainerRuntimeVersion: "containerd://2.2.0"},
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("8Gi")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}, {Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}, {Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse}, {Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse}},
				Images:      []corev1.ContainerImage{{Names: []string{benchmark.CandidateImage}}},
			},
		}
		nodes = append(nodes, node)
		objects = append(objects, node.DeepCopy())
		metrics = append(metrics, map[string]any{"metadata": map[string]string{"name": name}, "timestamp": benchmarkTestTime.Format(benchmarkTimestamp), "window": "30s", "usage": map[string]string{"cpu": "100m", "memory": "1Gi"}})
	}
	inventory, err := benchmarkInventory(nodes)
	if err != nil {
		t.Fatal(err)
	}
	basis, err := json.Marshal(map[string]any{"profileSha256": benchmark.ProfileSHA256, "nodeImage": benchmark.NodeImage, "kubeSystemUid": "00000000-0000-4000-8000-000000000001", "observedNodes": inventory})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := run.BenchmarkDigest(basis)
	descriptor, err := json.Marshal(map[string]any{"cluster": "perfeng-local", "fingerprint": fingerprint, "kubernetes": map[string]any{"version": "v1.35.0", "nodeCount": 3, "nodePools": []map[string]string{{"name": "control-plane", "cpuArchitecture": "amd64"}, {"name": "performance-generator", "cpuArchitecture": "amd64"}, {"name": "sut", "cpuArchitecture": "amd64"}}}, "application": map[string]string{"configurationHash": benchmark.ProfileSHA256}})
	if err != nil {
		t.Fatal(err)
	}
	spec := benchmark.Spec{
		ClusterID: "perfeng-local", ClusterUID: "00000000-0000-4000-8000-000000000001",
		Environment: baseline.Environment{Identity: rawresult.Identity{ID: "local-kind-integration", Version: "1.0.0", SHA256: run.BenchmarkDigest(descriptor)}, Fingerprint: fingerprint}, EnvironmentBytes: descriptor,
		Workload:       rawresult.Identity{ID: "pod-startup-contained", Version: "1.0.0", SHA256: benchmark.WorkloadSHA256},
		TemplateSHA256: benchmark.TemplateSHA256, RunnerImage: "ghcr.io/stanimirivanov/perfeng-kube-burner@sha256:" + strings.Repeat("a", 64), Bucket: "perfeng-artifacts",
	}
	if spec.Validate() != nil {
		t.Fatal("invalid synthetic selection")
	}
	metricBytes, err := json.Marshal(map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "NodeMetricsList", "items": metrics})
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset(objects...)
	client.PrependReactor("create", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		object := action.(clienttesting.CreateAction).GetObject().(metav1.Object)
		object.SetUID("00000000-0000-4000-8000-000000000099")
		object.SetResourceVersion("1")
		return false, nil, nil
	})
	controller, err := NewBenchmarkController(client, func(context.Context) ([]byte, error) { return metricBytes, nil }, "perf-generators", func() time.Time { return benchmarkTestTime })
	if err != nil {
		t.Fatal(err)
	}
	return controller, client, spec, metricBytes
}

func benchmarkReservation(t *testing.T, spec benchmark.Spec) run.BenchmarkReservation {
	t.Helper()
	plan := []byte(`{"approved":"synthetic"}`)
	return run.BenchmarkReservation{RunID: testRunID, Intent: run.BenchmarkIntent{TargetID: spec.ClusterID, ClusterUID: spec.ClusterUID, Plan: plan, PlanSHA256: run.BenchmarkDigest(plan)}, Admission: []byte(`{"status":"ACCEPTED"}`)}
}

func TestBenchmarkAdmissionRequiresFreshCompleteObservations(t *testing.T) {
	controller, client, spec, _ := benchmarkFixture(t)
	evidence, err := controller.Qualify(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Status         string
		Checks         []struct{ ID, Status string }
		Nodes, Samples []json.RawMessage
	}
	if err := json.Unmarshal(evidence, &document); err != nil {
		t.Fatal(err)
	}
	if document.Status != "ACCEPTED" || len(document.Checks) != 6 || len(document.Nodes) != 3 || len(document.Samples) != 3 {
		t.Fatalf("incomplete evidence: %s", evidence)
	}
	for _, check := range document.Checks {
		if check.Status != "PASS" {
			t.Fatal("accepted failed check")
		}
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatal("qualification mutated the target")
		}
	}
	for _, mutation := range []string{"missing metrics", "stale metrics", "pressure", "missing condition", "wrong placement", "cold image", "interference", "cluster replacement", "descriptor drift"} {
		t.Run(mutation, func(t *testing.T) {
			controller, client, spec, _ := benchmarkFixture(t)
			switch mutation {
			case "missing metrics":
				controller.metrics = func(context.Context) ([]byte, error) { return nil, errors.New("secret-bearing provider payload") }
			case "stale metrics":
				controller.now = func() time.Time { return benchmarkTestTime.Add(121 * time.Second) }
			case "pressure", "missing condition", "wrong placement", "cold image":
				node, err := client.CoreV1().Nodes().Get(context.Background(), "perfeng-local-worker2", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "pressure":
					node.Status.Conditions[1].Status = corev1.ConditionTrue
				case "missing condition":
					node.Status.Conditions = node.Status.Conditions[:1]
				case "wrong placement":
					node.Labels["workload"] = "performance-generator"
				case "cold image":
					node.Status.Images = nil
				}
				if _, err := client.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "interference":
				if _, err := client.CoreV1().Pods("perf-generators").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "another-run", Namespace: "perf-generators"}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "cluster replacement":
				spec.ClusterUID = "00000000-0000-4000-8000-000000000002"
			case "descriptor drift":
				spec.EnvironmentBytes[0] = 'x'
			}
			if _, err := controller.Qualify(context.Background(), spec); err == nil || strings.Contains(err.Error(), "secret-bearing") {
				t.Fatalf("unsafe admission result: %v", err)
			}
		})
	}
}

func TestBenchmarkResourcesAreExactCreateOnlyAndIncarnationBound(t *testing.T) {
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
	for range 2 {
		if err := controller.EnsureResources(context.Background(), spec, reservation, fence); err != nil {
			t.Fatal(err)
		}
	}
	creates := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "create" {
			creates++
		}
		if action.GetVerb() == "update" || action.GetVerb() == "delete" || action.GetVerb() == "patch" {
			t.Fatal("adoption repaired authority")
		}
	}
	if creates != 8 {
		t.Fatalf("created %d objects, want namespace and seven scoped resources", creates)
	}
	name := benchmark.ResourceName(testRunID)
	role, err := client.RbacV1().Roles(benchmark.Namespace(testRunID)).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 2 || strings.Join(role.Rules[0].Verbs, ",") != "create" || strings.Join(role.Rules[0].Resources, ",") != "pods" {
		t.Fatal("runner write authority expanded")
	}
	role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}})
	if _, err := client.RbacV1().Roles(role.Namespace).Update(context.Background(), role, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnsureResources(context.Background(), spec, reservation, fence); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("expanded RBAC adopted: %v", err)
	}
	if err := client.CoreV1().Namespaces().Delete(context.Background(), benchmark.Namespace(testRunID), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	if _, err := controller.EnsureNamespace(context.Background(), reservation, fence); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("missing bound namespace replaced: %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "create" {
			t.Fatal("recreated a bound namespace")
		}
	}
}

func TestBenchmarkTemplateUsesExistingUploaderSecretKeys(t *testing.T) {
	_, _, spec, _ := benchmarkFixture(t)
	template := BenchmarkJobTemplate(spec.RunnerImage, "registry", "perfeng-s3-uploader-auth", "http://seaweedfs.perf-platform.svc.cluster.local:8333")
	if err := ValidateBenchmarkTemplate(template, spec.RunnerImage); err != nil {
		t.Fatal(err)
	}
	environment := template.Spec.Template.Spec.Containers[0].Env
	for index, expected := range []struct{ name, key string }{
		{"AWS_ACCESS_KEY_ID", "access-key"},
		{"AWS_SECRET_ACCESS_KEY", "secret-key"},
	} {
		value := environment[index+1]
		if value.Name != expected.name || value.Value != "" || value.ValueFrom == nil || value.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("publisher credential must be an explicit Secret reference: %s", expected.name)
		}
		reference := value.ValueFrom.SecretKeyRef
		if reference.Name != "perfeng-s3-uploader-auth" || reference.Key != expected.key {
			t.Fatalf("publisher credential reference differs: %+v", reference)
		}
	}
	for index, key := range []string{"access-key-id", "secret-access-key"} {
		t.Run(key, func(t *testing.T) {
			altered := template.DeepCopy()
			altered.Spec.Template.Spec.Containers[0].Env[index+1].ValueFrom.SecretKeyRef.Key = key
			if err := ValidateBenchmarkTemplate(altered, spec.RunnerImage); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("incorrect uploader key accepted: %v", err)
			}
		})
	}
}

func TestBenchmarkTemplateStorageEndpointBoundary(t *testing.T) {
	_, _, spec, _ := benchmarkFixture(t)
	for _, endpoint := range []string{
		"http://seaweedfs.perf-platform.svc.cluster.local:8333",
		"https://storage.example.test",
		"https://storage.example.test:9443/",
	} {
		t.Run(endpoint, func(t *testing.T) {
			template := BenchmarkJobTemplate(spec.RunnerImage, "registry", "storage", endpoint)
			if err := ValidateBenchmarkTemplate(template, spec.RunnerImage); err != nil {
				t.Fatalf("reviewed endpoint rejected: %v", err)
			}
		})
	}
	for _, endpoint := range []string{
		"http://seaweedfs-s3.perf-platform.svc.cluster.local:8333",
		"http://storage.example.test:8333",
		"http://seaweedfs.perf-platform.svc.cluster.local.evil.test:8333",
		"http://seaweedfs.perf-platform.svc.cluster.local:8334",
		"http://seaweedfs.perf-platform.svc.cluster.local:8333/",
		"http://seaweedfs.perf-platform.svc.cluster.local:8333?token=value",
		"http://seaweedfs.perf-platform.svc.cluster.local:8333#fragment",
		"https://user:password@storage.example.test",
		"https://storage.example.test/bucket",
		"https://storage.example.test?token=value",
		"https://storage.example.test#fragment",
	} {
		t.Run(endpoint, func(t *testing.T) {
			template := BenchmarkJobTemplate(spec.RunnerImage, "registry", "storage", endpoint)
			if err := ValidateBenchmarkTemplate(template, spec.RunnerImage); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("unreviewed endpoint accepted: %v", err)
			}
		})
	}
}

func TestBenchmarkTemplateRejectsCapabilityExpansionAndBindsExactInputs(t *testing.T) {
	_, _, spec, _ := benchmarkFixture(t)
	template := BenchmarkJobTemplate(spec.RunnerImage, "registry", "storage", "http://seaweedfs.perf-platform.svc.cluster.local:8333")
	if err := ValidateBenchmarkTemplate(template, spec.RunnerImage); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*batchv1.Job){
		"command": func(job *batchv1.Job) { job.Spec.Template.Spec.InitContainers[0].Command = []string{"sh"} },
		"publisher token": func(job *batchv1.Job) {
			job.Spec.Template.Spec.Containers[0].VolumeMounts = append(job.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "api", MountPath: "/token"})
		},
		"unbounded quota": func(job *batchv1.Job) { job.Spec.ActiveDeadlineSeconds = nil },
		"privilege":       func(job *batchv1.Job) { job.Spec.Template.Spec.HostNetwork = true },
		"extra env": func(job *batchv1.Job) {
			job.Spec.Template.Spec.InitContainers[0].Env = append(job.Spec.Template.Spec.InitContainers[0].Env, corev1.EnvVar{Name: "AWS_SECRET_ACCESS_KEY", Value: "forbidden"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			altered := template.DeepCopy()
			mutate(altered)
			if err := ValidateBenchmarkTemplate(altered, spec.RunnerImage); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("expanded template accepted: %v", err)
			}
		})
	}
	reservation := benchmarkReservation(t, spec)
	reservation.NamespaceUID = "00000000-0000-4000-8000-000000000099"
	var err error
	reservation.Request, err = benchmark.RequestBytes(spec, testRunID, reservation.NamespaceUID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := BindBenchmarkJob(template, spec, reservation)
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Template.Spec.ServiceAccountName != benchmark.ResourceName(testRunID) || job.Spec.Template.Spec.InitContainers[0].Args[4] != run.BenchmarkDigest(reservation.Request) || template.Spec.Template.Spec.ServiceAccountName != "benchmark-executor" {
		t.Fatal("bound Job drifted or aliased reusable template")
	}
	reservation.Request = append(reservation.Request, ' ')
	if _, err := BindBenchmarkJob(template, spec, reservation); !errors.Is(err, run.ErrBenchmarkConflict) {
		t.Fatalf("different sealed bytes admitted: %v", err)
	}
}

func TestBenchmarkNamespaceRejectsForeignOwnershipAndUnexpectedAdmissionLabels(t *testing.T) {
	for _, mutation := range []string{"foreign owner", "injection label", "replacement UID"} {
		t.Run(mutation, func(t *testing.T) {
			controller, client, spec, _ := benchmarkFixture(t)
			reservation := benchmarkReservation(t, spec)
			namespace := benchmarkNamespace(reservation)
			switch mutation {
			case "foreign owner":
				namespace.Labels[managedByLabel] = "foreign"
			case "injection label":
				namespace.Labels["istio-injection"] = "enabled"
			case "replacement UID":
				reservation.NamespaceUID = "00000000-0000-4000-8000-000000000004"
			}
			if _, err := client.CoreV1().Namespaces().Create(context.Background(), namespace, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			if _, err := controller.EnsureNamespace(context.Background(), reservation, func(context.Context) error { return nil }); !errors.Is(err, run.ErrBenchmarkConflict) {
				t.Fatalf("unreviewed namespace adopted: %v", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("foreign namespace was modified: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestBenchmarkMetricsRejectMissingFutureDuplicateAndOverflowedUsage(t *testing.T) {
	for _, mutation := range []string{"missing node", "duplicate node", "future", "oversized window", "high cpu", "overflow cpu", "negative usage", "missing usage"} {
		t.Run(mutation, func(t *testing.T) {
			controller, _, spec, data := benchmarkFixture(t)
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			items := document["items"].([]any)
			first := items[0].(map[string]any)
			usage := first["usage"].(map[string]any)
			switch mutation {
			case "missing node":
				document["items"] = items[:2]
			case "duplicate node":
				items[1] = first
			case "future":
				first["timestamp"] = benchmarkTestTime.Add(time.Second).Format(benchmarkTimestamp)
			case "oversized window":
				first["window"] = "61s"
			case "high cpu":
				usage["cpu"] = "3001m"
			case "overflow cpu":
				usage["cpu"] = "1e100"
			case "negative usage":
				usage["memory"] = "-1"
			case "missing usage":
				delete(usage, "cpu")
			}
			changed, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			controller.metrics = func(context.Context) ([]byte, error) { return changed, nil }
			if _, err := controller.Qualify(context.Background(), spec); !errors.Is(err, benchmark.ErrNotQualified) {
				t.Fatalf("unqualified usage admitted: %v", err)
			}
		})
	}
}
