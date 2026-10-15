package kubernetes

import (
	"net/url"
	"reflect"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const benchmarkRequestPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"

// BenchmarkJobTemplate describes the contained PR3 runner/publisher protocol, not
// an arbitrary kube-burner command. Names are replaced only after durable sealing.
// Credentials remain external Secret references and are never read by this adapter.
func BenchmarkJobTemplate(image, pullSecret, storageSecret, endpoint string) *batchv1.Job {
	security := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	inputs := []corev1.VolumeMount{
		{Name: "inputs", MountPath: "/inputs/request.json", SubPath: "request.json", ReadOnly: true},
		{Name: "inputs", MountPath: "/inputs/environment.json", SubPath: "environment.json", ReadOnly: true},
	}
	runner := corev1.Container{
		Name: "benchmark", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{"run", "--request", "/inputs/request.json", "--request-sha256", benchmarkRequestPlaceholder},
		Env: []corev1.EnvVar{{Name: "PERFENG_EXECUTOR_POD_UID", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.uid"},
		}}},
		SecurityContext: security.DeepCopy(),
		Resources:       benchmarkResources("500m", "256Mi", "128Mi", "1", "512Mi", "256Mi"),
		VolumeMounts: append(append([]corev1.VolumeMount{}, inputs...),
			corev1.VolumeMount{Name: "api", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true},
			corev1.VolumeMount{Name: "results", MountPath: "/results"},
			corev1.VolumeMount{Name: "runner-tmp", MountPath: "/tmp"}),
	}
	publisher := corev1.Container{
		Name: "publish-raw", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{"publish", "--request", "/inputs/request.json", "--request-sha256", benchmarkRequestPlaceholder},
		Env: []corev1.EnvVar{
			{Name: "PERFENG_S3_ENDPOINT", Value: endpoint},
			{Name: "AWS_ACCESS_KEY_ID", ValueFrom: benchmarkSecret(storageSecret, "access-key")},
			{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: benchmarkSecret(storageSecret, "secret-key")},
		},
		SecurityContext: security.DeepCopy(),
		Resources:       benchmarkResources("100m", "128Mi", "64Mi", "500m", "256Mi", "256Mi"),
		VolumeMounts: append(append([]corev1.VolumeMount{}, inputs...),
			corev1.VolumeMount{Name: "results", MountPath: "/results", ReadOnly: true},
			corev1.VolumeMount{Name: "publisher-tmp", MountPath: "/tmp"}),
	}
	job := &batchv1.Job{Spec: batchv1.JobSpec{
		BackoffLimit: ptr.To[int32](0), Completions: ptr.To[int32](1), Parallelism: ptr.To[int32](1),
		ActiveDeadlineSeconds: ptr.To[int64](600),
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks: ptr.To(false), ServiceAccountName: "benchmark-executor",
			TerminationGracePeriodSeconds: ptr.To[int64](15),
			ImagePullSecrets:              []corev1.LocalObjectReference{{Name: pullSecret}},
			NodeSelector:                  map[string]string{"workload": "performance-generator", "kubernetes.io/os": "linux", "kubernetes.io/arch": "amd64"},
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532), FSGroup: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			InitContainers: []corev1.Container{runner}, Containers: []corev1.Container{publisher},
			Volumes: []corev1.Volume{
				{Name: "inputs", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "benchmark-inputs"}, DefaultMode: ptr.To[int32](0644)}}},
				{Name: "api", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: ptr.To[int32](0644), Sources: []corev1.VolumeProjection{
					{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: ptr.To[int64](600)}},
					{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
				}}}},
				benchmarkEmptyDir("results", "128Mi"), benchmarkEmptyDir("runner-tmp", "32Mi"), benchmarkEmptyDir("publisher-tmp", "32Mi"),
			},
		}},
	}}
	return job
}

// ValidateBenchmarkTemplate rejects any capability outside the contained recipe,
// including extra containers, commands, credentials, mounts, selectors and hooks.
func ValidateBenchmarkTemplate(template *batchv1.Job, image string) error {
	if ValidateReusableJobTemplate(template) != nil {
		return run.ErrValidation
	}
	pod := template.Spec.Template.Spec
	if len(pod.ImagePullSecrets) != 1 || len(pod.Containers) != 1 || len(pod.Containers[0].Env) != 3 {
		return run.ErrValidation
	}
	pullSecret := pod.ImagePullSecrets[0].Name
	environment := pod.Containers[0].Env
	if environment[1].ValueFrom == nil || environment[1].ValueFrom.SecretKeyRef == nil {
		return run.ErrValidation
	}
	storageSecret := environment[1].ValueFrom.SecretKeyRef.Name
	endpoint, err := url.Parse(environment[0].Value)
	if err != nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") ||
		(endpoint.Scheme != "https" && environment[0].Value != "http://seaweedfs.perf-platform.svc.cluster.local:8333") || endpoint.Host == "" ||
		len(validation.IsDNS1123Subdomain(pullSecret)) != 0 || len(validation.IsDNS1123Subdomain(storageSecret)) != 0 {
		return run.ErrValidation
	}
	expected := BenchmarkJobTemplate(image, pullSecret, storageSecret, environment[0].Value)
	if !reflect.DeepEqual(template, expected) {
		return run.ErrValidation
	}
	return nil
}

// BindBenchmarkJob assigns only the names and hash derived from the sealed hold.
// The caller still owns lease fencing and deterministic Job create/adoption.
func BindBenchmarkJob(template *batchv1.Job, spec benchmark.Spec, reservation run.BenchmarkReservation) (*batchv1.Job, error) {
	if !contract.ValidID(reservation.RunID) || spec.Validate() != nil ||
		ValidateBenchmarkTemplate(template, spec.RunnerImage) != nil || !run.ValidBenchmarkUID(reservation.NamespaceUID) {
		return nil, run.ErrValidation
	}
	expected, err := benchmark.RequestBytes(spec, reservation.RunID, reservation.NamespaceUID)
	if err != nil || string(expected) != string(reservation.Request) ||
		spec.ClusterID != reservation.Intent.TargetID || spec.ClusterUID != reservation.Intent.ClusterUID {
		return nil, run.ErrBenchmarkConflict
	}
	job := template.DeepCopy()
	pod := &job.Spec.Template.Spec
	pod.ServiceAccountName = benchmark.ResourceName(reservation.RunID)
	pod.Volumes[0].ConfigMap.Name = benchmark.ResourceName(reservation.RunID)
	pod.InitContainers[0].Args[4] = run.BenchmarkDigest(reservation.Request)
	pod.Containers[0].Args[4] = run.BenchmarkDigest(reservation.Request)
	return job, nil
}

func benchmarkSecret(name, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}}
}

func benchmarkEmptyDir(name, size string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse(size))}}}
}

func benchmarkResources(cpu, memory, disk, maxCPU, maxMemory, maxDisk string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory), corev1.ResourceEphemeralStorage: resource.MustParse(disk)},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(maxCPU), corev1.ResourceMemory: resource.MustParse(maxMemory), corev1.ResourceEphemeralStorage: resource.MustParse(maxDisk)},
	}
}
