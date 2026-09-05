// Package kubernetes provides the control-plane boundary for Kubernetes workloads.
package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	runLabel       = "perfeng.io/run-id"
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "perfeng-control-plane"
	specAnnotation = "perfeng.io/execution-spec-sha256"
)

var imagePattern = regexp.MustCompile(
	`^[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)+@sha256:[a-f0-9]{64}$`,
)

// ErrJobConflict means a run's deterministic Job name is already bound to a
// different owner or execution specification. It must not be deleted or replaced.
var ErrJobConflict = errors.New("kubernetes Job identity conflicts with the requested execution")

// Jobs is the Kubernetes API boundary for dispatch, observation and stop requests.
// A typed client-go JobInterface satisfies this contract.
type Jobs interface {
	// Create must surface AlreadyExists without replacing the existing Job.
	Create(context.Context, *batchv1.Job, metav1.CreateOptions) (*batchv1.Job, error)
	// Get reads from the client's bound namespace and preserves NotFound errors.
	Get(context.Context, string, metav1.GetOptions) (*batchv1.Job, error)
	// Delete must honor supplied UID preconditions; NotFound means already absent.
	Delete(context.Context, string, metav1.DeleteOptions) error
}

// Dispatcher creates or adopts one deterministic Kubernetes Job per Run.
type Dispatcher struct {
	jobs      Jobs
	namespace string
}

// Dispatch identifies the Kubernetes Job accepted for a Run. Created is false
// when an earlier, matching create was adopted after a retry or worker restart.
type Dispatch struct {
	RunID      string
	Namespace  string
	JobName    string
	UID        types.UID
	SpecSHA256 string
	Created    bool
}

// Execution returns the durable subset of the accepted dispatch result.
func (dispatch Dispatch) Execution() Execution {
	return Execution{
		RunID:      dispatch.RunID,
		Namespace:  dispatch.Namespace,
		JobName:    dispatch.JobName,
		UID:        dispatch.UID,
		SpecSHA256: dispatch.SpecSHA256,
	}
}

// NewDispatcher binds dispatch to one namespace and an injected Kubernetes client.
func NewDispatcher(jobs Jobs, namespace string) (*Dispatcher, error) {
	if jobs == nil {
		return nil, errors.New("kubernetes Jobs client is required")
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) != 0 {
		return nil, errors.New("valid Kubernetes namespace is required")
	}

	return &Dispatcher{jobs: jobs, namespace: namespace}, nil
}

// ValidateReusableJobTemplate applies execution policy before the control plane
// assigns a Run identity and namespace. Reusable templates must not carry an
// identity from an earlier execution.
func ValidateReusableJobTemplate(template *batchv1.Job) error {
	if template == nil || template.Name != "" || template.GenerateName != "" ||
		template.Namespace != "" || hasControlPlaneIdentity(template.Labels) ||
		hasControlPlaneIdentity(template.Spec.Template.Labels) ||
		template.Annotations[specAnnotation] != "" ||
		template.Spec.Template.Annotations[specAnnotation] != "" {
		return run.ErrValidation
	}

	return validateJob(template)
}

// ValidateJob applies the dispatcher's identity and execution policy without
// contacting Kubernetes. The caller retains ownership of its template.
func (dispatcher *Dispatcher) ValidateJob(runID string, template *batchv1.Job) error {
	_, err := dispatcher.prepare(runID, template)

	return err
}

// EnsureJob creates the Run's Job or adopts the existing Job only when its
// control-plane identity, fingerprint and requested specification match.
// Already-existing conflicts are never deleted or replaced.
func (dispatcher *Dispatcher) EnsureJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (Dispatch, error) {
	desired, err := dispatcher.prepare(runID, template)
	if err != nil {
		return Dispatch{}, err
	}

	created, err := dispatcher.jobs.Create(ctx, desired, metav1.CreateOptions{})
	if err == nil {
		return acceptedDispatch(desired, created, runID, dispatcher.namespace, true)
	}
	if !apierrors.IsAlreadyExists(err) {
		return Dispatch{}, classifyAPIError("create", err)
	}

	existing, err := dispatcher.jobs.Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Dispatch{}, run.ErrUnavailable
		}
		return Dispatch{}, classifyAPIError("get", err)
	}
	if !matchingJob(desired, existing) {
		return Dispatch{}, ErrJobConflict
	}

	return acceptedDispatch(desired, existing, runID, dispatcher.namespace, false)
}

func (dispatcher *Dispatcher) prepare(runID string, template *batchv1.Job) (*batchv1.Job, error) {
	if !contract.ValidID(runID) || template == nil {
		return nil, run.ErrValidation
	}
	if template.GenerateName != "" ||
		(template.Name != "" && template.Name != runID) ||
		(template.Namespace != "" && template.Namespace != dispatcher.namespace) {
		return nil, run.ErrValidation
	}
	if !identityCompatible(template.Labels, runID) ||
		!identityCompatible(template.Spec.Template.Labels, runID) ||
		template.Annotations[specAnnotation] != "" ||
		template.Spec.Template.Annotations[specAnnotation] != "" {
		return nil, run.ErrValidation
	}
	if err := validateJob(template); err != nil {
		return nil, err
	}

	desired := template.DeepCopy()
	desired.APIVersion = "batch/v1"
	desired.Kind = "Job"
	desired.Name = runID
	desired.Namespace = dispatcher.namespace
	desired.Labels = withIdentity(desired.Labels, runID)
	desired.Spec.Template.Labels = withIdentity(desired.Spec.Template.Labels, runID)
	desired.Annotations = withoutKey(desired.Annotations, specAnnotation)
	desired.Spec.Template.Annotations = withoutKey(
		desired.Spec.Template.Annotations,
		specAnnotation,
	)
	fingerprint, err := jobFingerprint(desired)
	if err != nil {
		return nil, errors.New("could not fingerprint Kubernetes Job specification")
	}
	desired.Annotations = withValue(desired.Annotations, specAnnotation, fingerprint)
	desired.Spec.Template.Annotations = withValue(
		desired.Spec.Template.Annotations,
		specAnnotation,
		fingerprint,
	)

	return desired, nil
}

func validateJob(job *batchv1.Job) error {
	pod := job.Spec.Template.Spec
	if job.ResourceVersion != "" || job.UID != "" || job.DeletionTimestamp != nil ||
		len(job.OwnerReferences) != 0 || len(job.Finalizers) != 0 {
		return run.ErrValidation
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 ||
		job.Spec.Completions == nil || *job.Spec.Completions != 1 ||
		job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 ||
		job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds < 1 {
		return run.ErrValidation
	}
	if job.Spec.ManualSelector != nil && *job.Spec.ManualSelector {
		return run.ErrValidation
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever ||
		pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken ||
		pod.HostNetwork || pod.HostPID || pod.HostIPC || len(pod.Containers) == 0 {
		return run.ErrValidation
	}
	for _, volume := range pod.Volumes {
		if volume.HostPath != nil {
			return run.ErrValidation
		}
	}
	for _, container := range append(append([]corev1.Container{}, pod.InitContainers...), pod.Containers...) {
		if !imagePattern.MatchString(container.Image) {
			return run.ErrValidation
		}
	}

	return nil
}

func hasControlPlaneIdentity(values map[string]string) bool {
	return values[runLabel] != "" || values[managedByLabel] != "" || values[stageLabel] != ""
}

func matchingJob(desired, existing *batchv1.Job) bool {
	if existing == nil || existing.Name != desired.Name || existing.Namespace != desired.Namespace {
		return false
	}
	if existing.Labels[runLabel] != desired.Labels[runLabel] ||
		existing.Labels[managedByLabel] != managedByValue ||
		existing.Annotations[specAnnotation] != desired.Annotations[specAnnotation] {
		return false
	}

	return apiequality.Semantic.DeepEqual(normalizedSpec(desired), normalizedSpec(existing))
}

func jobFingerprint(job *batchv1.Job) (string, error) {
	withoutFingerprint := job.DeepCopy()
	delete(withoutFingerprint.Annotations, specAnnotation)
	delete(withoutFingerprint.Spec.Template.Annotations, specAnnotation)
	encoded, err := json.Marshal(normalizedSpec(withoutFingerprint))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func normalizedSpec(job *batchv1.Job) batchv1.JobSpec {
	normalized := job.DeepCopy()
	normalized.Spec.Selector = nil
	clearAPIServerDefaults(&normalized.Spec)
	for _, key := range []string{
		batchv1.ControllerUidLabel,
		batchv1.JobNameLabel,
		"controller-uid",
		"job-name",
	} {
		delete(normalized.Spec.Template.Labels, key)
	}

	return normalized.Spec
}

func clearAPIServerDefaults(spec *batchv1.JobSpec) {
	if spec.ManualSelector != nil && !*spec.ManualSelector {
		spec.ManualSelector = nil
	}
	if spec.CompletionMode != nil && *spec.CompletionMode == batchv1.NonIndexedCompletion {
		spec.CompletionMode = nil
	}
	if spec.Suspend != nil && !*spec.Suspend {
		spec.Suspend = nil
	}
	if spec.PodReplacementPolicy != nil {
		policy := *spec.PodReplacementPolicy
		if policy == batchv1.TerminatingOrFailed ||
			(policy == batchv1.Failed && spec.PodFailurePolicy != nil) {
			spec.PodReplacementPolicy = nil
		}
	}

	pod := &spec.Template.Spec
	if pod.TerminationGracePeriodSeconds != nil && *pod.TerminationGracePeriodSeconds == 30 {
		pod.TerminationGracePeriodSeconds = nil
	}
	if pod.DNSPolicy == corev1.DNSClusterFirst {
		pod.DNSPolicy = ""
	}
	if pod.SchedulerName == corev1.DefaultSchedulerName {
		pod.SchedulerName = ""
	}
	if apiequality.Semantic.DeepEqual(pod.SecurityContext, &corev1.PodSecurityContext{}) {
		pod.SecurityContext = nil
	}
	for index := range pod.InitContainers {
		clearContainerDefaults(&pod.InitContainers[index])
	}
	for index := range pod.Containers {
		clearContainerDefaults(&pod.Containers[index])
	}
}

func clearContainerDefaults(container *corev1.Container) {
	if container.TerminationMessagePath == corev1.TerminationMessagePathDefault {
		container.TerminationMessagePath = ""
	}
	if container.TerminationMessagePolicy == corev1.TerminationMessageReadFile {
		container.TerminationMessagePolicy = ""
	}
	if container.ImagePullPolicy == corev1.PullIfNotPresent {
		container.ImagePullPolicy = ""
	}
}

func acceptedDispatch(
	desired *batchv1.Job,
	actual *batchv1.Job,
	runID string,
	namespace string,
	created bool,
) (Dispatch, error) {
	if actual == nil {
		return Dispatch{}, run.ErrUnavailable
	}
	if !matchingJob(desired, actual) {
		return Dispatch{}, ErrJobConflict
	}
	if actual.Name != runID || actual.Namespace != namespace || actual.UID == "" {
		return Dispatch{}, run.ErrUnavailable
	}

	return Dispatch{
		RunID:      runID,
		Namespace:  actual.Namespace,
		JobName:    actual.Name,
		UID:        actual.UID,
		SpecSHA256: desired.Annotations[specAnnotation],
		Created:    created,
	}, nil
}

func classifyAPIError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) ||
		apierrors.IsTooManyRequests(err) || apierrors.IsServiceUnavailable(err) ||
		apierrors.IsInternalError(err) {
		return run.ErrUnavailable
	}

	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Reason != "" {
		return &kubernetesError{operation: operation, reason: status.Status().Reason}
	}

	return &kubernetesError{operation: operation}
}

type kubernetesError struct {
	operation string
	reason    metav1.StatusReason
}

func (err *kubernetesError) Error() string {
	if err.reason == "" {
		return fmt.Sprintf("kubernetes %s failed", err.operation)
	}

	return fmt.Sprintf("kubernetes %s failed (%s)", err.operation, err.reason)
}

func withIdentity(values map[string]string, runID string) map[string]string {
	result := make(map[string]string, len(values)+2)
	for key, value := range values {
		result[key] = value
	}
	result[runLabel] = runID
	result[managedByLabel] = managedByValue

	return result
}

func identityCompatible(values map[string]string, runID string) bool {
	return (values[runLabel] == "" || values[runLabel] == runID) &&
		(values[managedByLabel] == "" || values[managedByLabel] == managedByValue)
}

func withValue(values map[string]string, key, value string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for existingKey, existingValue := range values {
		result[existingKey] = existingValue
	}
	result[key] = value

	return result
}

func withoutKey(values map[string]string, omitted string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		if key != omitted {
			result[key] = value
		}
	}

	return result
}
