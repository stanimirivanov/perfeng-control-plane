package kubernetes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const testRunID = "perf-20260903-120000-12345678"

type memoryJobs struct {
	mu             sync.Mutex
	job            *batchv1.Job
	createError    error
	getError       error
	storeThenError error
	mutateStored   func(*batchv1.Job)
	deleteError    error
	deleteOptions  *metav1.DeleteOptions
	beforeDelete   func(*batchv1.Job)
	keepDeleting   bool
	creates        int
	gets           int
	deletes        int
}

func (client *memoryJobs) Create(
	ctx context.Context,
	job *batchv1.Job,
	_ metav1.CreateOptions,
) (*batchv1.Job, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.creates++

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client.createError != nil {
		return nil, client.createError
	}
	if client.job != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, job.Name)
	}

	client.job = job.DeepCopy()
	client.job.UID = types.UID("job-uid")
	if client.mutateStored != nil {
		client.mutateStored(client.job)
	}
	if client.storeThenError != nil {
		err := client.storeThenError
		client.storeThenError = nil

		return nil, err
	}

	return client.job.DeepCopy(), nil
}

func (client *memoryJobs) Get(
	ctx context.Context,
	name string,
	_ metav1.GetOptions,
) (*batchv1.Job, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.gets++

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client.getError != nil {
		return nil, client.getError
	}
	if client.job == nil || client.job.Name != name {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, name)
	}

	return client.job.DeepCopy(), nil
}

func (client *memoryJobs) Delete(
	ctx context.Context,
	name string,
	options metav1.DeleteOptions,
) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.deletes++
	client.deleteOptions = options.DeepCopy()

	if err := ctx.Err(); err != nil {
		return err
	}
	if client.deleteError != nil {
		return client.deleteError
	}
	if client.job == nil || client.job.Name != name {
		return apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, name)
	}
	if client.beforeDelete != nil {
		client.beforeDelete(client.job)
	}
	if options.Preconditions == nil || options.Preconditions.UID == nil ||
		*options.Preconditions.UID != client.job.UID {
		return apierrors.NewConflict(
			schema.GroupResource{Group: "batch", Resource: "jobs"},
			name,
			errors.New("UID precondition failed"),
		)
	}

	if client.keepDeleting {
		now := metav1.Now()
		client.job.DeletionTimestamp = &now
	} else {
		client.job = nil
	}

	return nil
}

func jobTemplate() *batchv1.Job {
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false

	return &batchv1.Job{
		Spec: batchv1.JobSpec{
			BackoffLimit:          &zero,
			Completions:           &one,
			Parallelism:           &one,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					Containers: []corev1.Container{{
						Name:  "runner",
						Image: "ghcr.io/example/perfeng-k6@sha256:" + strings.Repeat("a", 64),
						Args:  []string{"run", "tests/checkout/scenario.js"},
					}},
				},
			},
		},
	}
}

func newTestDispatcher(t *testing.T, jobs Jobs) *Dispatcher {
	t.Helper()
	dispatcher, err := NewDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}

	return dispatcher
}

func TestEnsureJobCreatesThenAdopts(t *testing.T) {
	client := &memoryJobs{mutateStored: addServerIdentity}
	dispatcher := newTestDispatcher(t, client)
	template := jobTemplate()

	created, err := dispatcher.EnsureJob(context.Background(), testRunID, template)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.JobName != testRunID || created.UID == "" {
		t.Fatal("new Job identity was not returned")
	}
	if template.Name != "" || template.Labels != nil {
		t.Fatal("caller template was mutated")
	}
	adopted, err := dispatcher.EnsureJob(context.Background(), testRunID, template)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Created || adopted.RunID != testRunID || adopted.Namespace != "perf-runs" ||
		adopted.JobName != testRunID || adopted.UID != "job-uid" ||
		!fingerprintPattern.MatchString(adopted.SpecSHA256) {
		t.Fatal("matching Job was not adopted")
	}
	if client.creates != 2 || client.gets != 1 {
		t.Fatal("unexpected Kubernetes API call sequence")
	}
}

func addServerIdentity(job *batchv1.Job) {
	job.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{
		batchv1.ControllerUidLabel: "job-uid",
	}}
	job.Spec.Template.Labels[batchv1.ControllerUidLabel] = "job-uid"
	job.Spec.Template.Labels[batchv1.JobNameLabel] = testRunID
	job.Spec.Template.Labels["controller-uid"] = "job-uid"
	job.Spec.Template.Labels["job-name"] = testRunID
}

func addAPIServerDefaults(job *batchv1.Job) {
	manualSelector, suspend := false, false
	completionMode := batchv1.NonIndexedCompletion
	podReplacementPolicy := batchv1.TerminatingOrFailed
	terminationGracePeriod := int64(30)
	job.Spec.ManualSelector = &manualSelector
	job.Spec.CompletionMode = &completionMode
	job.Spec.Suspend = &suspend
	job.Spec.PodReplacementPolicy = &podReplacementPolicy
	job.Spec.Template.Spec.TerminationGracePeriodSeconds = &terminationGracePeriod
	job.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
	job.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
	job.Spec.Template.Spec.SchedulerName = corev1.DefaultSchedulerName
	for index := range job.Spec.Template.Spec.Containers {
		container := &job.Spec.Template.Spec.Containers[index]
		container.TerminationMessagePath = corev1.TerminationMessagePathDefault
		container.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		container.ImagePullPolicy = corev1.PullIfNotPresent
	}
}

func TestEnsureJobAcceptsAPIServerDefaults(t *testing.T) {
	client := &memoryJobs{mutateStored: addAPIServerDefaults}
	dispatcher := newTestDispatcher(t, client)
	created, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.ObserveJob(context.Background(), created.Execution()); err != nil {
		t.Fatal(err)
	}
	adopted, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate())
	if err != nil || adopted.Created || adopted.UID != created.UID {
		t.Fatal("API-server-defaulted Job was not adopted", err)
	}
}

func TestNormalizationKeepsExplicitReplacementPolicy(t *testing.T) {
	desired := jobTemplate()
	policy := batchv1.Failed
	desired.Spec.PodReplacementPolicy = &policy
	defaulted := desired.DeepCopy()
	defaultPolicy := batchv1.TerminatingOrFailed
	defaulted.Spec.PodReplacementPolicy = &defaultPolicy
	if apiequality.Semantic.DeepEqual(normalizedSpec(desired), normalizedSpec(defaulted)) {
		t.Fatal("explicit replacement policy was normalized as an API-server default")
	}

	desiredWithFailurePolicy := jobTemplate()
	desiredWithFailurePolicy.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{}
	defaultedWithFailurePolicy := desiredWithFailurePolicy.DeepCopy()
	defaultedWithFailurePolicy.Spec.PodReplacementPolicy = &policy
	if !apiequality.Semantic.DeepEqual(
		normalizedSpec(desiredWithFailurePolicy),
		normalizedSpec(defaultedWithFailurePolicy),
	) {
		t.Fatal("conditional replacement-policy default was not normalized")
	}
}

func TestEnsureJobRejectsNondefaultServerChanges(t *testing.T) {
	for name, mutate := range map[string]func(*batchv1.Job){
		"scheduler": func(job *batchv1.Job) { job.Spec.Template.Spec.SchedulerName = "other" },
		"pull-policy": func(job *batchv1.Job) {
			job.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
		},
		"security-context": func(job *batchv1.Job) {
			job.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: new(true)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &memoryJobs{mutateStored: mutate}
			_, err := newTestDispatcher(t, client).EnsureJob(context.Background(), testRunID, jobTemplate())
			if !errors.Is(err, ErrJobConflict) {
				t.Fatal("nondefault server change was accepted", err)
			}
		})
	}
}

func TestEnsureJobConcurrentReconcilersConverge(t *testing.T) {
	client := &memoryJobs{}
	dispatcher := newTestDispatcher(t, client)

	const workers = 20
	results := make(chan Dispatch, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Go(func() {
			result, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate())
			results <- result
			errorsFound <- err
		})
	}
	wait.Wait()
	close(results)
	close(errorsFound)

	created := 0
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.UID != "job-uid" {
			t.Fatal("reconcilers did not converge on one Job UID")
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d Jobs, want 1", created)
	}
}

func TestEnsureJobRejectsAdmissionMutation(t *testing.T) {
	client := &memoryJobs{mutateStored: func(job *batchv1.Job) {
		job.Spec.Template.Spec.Containers[0].Args = []string{"run", "tests/search/scenario.js"}
	}}
	_, err := newTestDispatcher(t, client).EnsureJob(context.Background(), testRunID, jobTemplate())
	if !errors.Is(err, ErrJobConflict) {
		t.Fatal("mutated create response was accepted", err)
	}
}

func TestEnsureJobRecoversAmbiguousCreate(t *testing.T) {
	client := &memoryJobs{
		storeThenError: apierrors.NewServerTimeout(
			schema.GroupResource{Group: "batch", Resource: "jobs"},
			"create",
			1,
		),
	}
	dispatcher := newTestDispatcher(t, client)
	template := jobTemplate()

	if _, err := dispatcher.EnsureJob(context.Background(), testRunID, template); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("ambiguous create was not retryable", err)
	}
	adopted, err := dispatcher.EnsureJob(context.Background(), testRunID, template)
	if err != nil || adopted.Created {
		t.Fatal("persisted Job was not adopted after ambiguous create", err)
	}
}

func TestEnsureJobRejectsIdentityAndSpecificationConflicts(t *testing.T) {
	client := &memoryJobs{}
	dispatcher := newTestDispatcher(t, client)
	if _, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate()); err != nil {
		t.Fatal(err)
	}

	changed := jobTemplate()
	changed.Spec.Template.Spec.Containers[0].Args = []string{"run", "tests/search/scenario.js"}
	if _, err := dispatcher.EnsureJob(context.Background(), testRunID, changed); !errors.Is(err, ErrJobConflict) {
		t.Fatal("different execution specification was adopted", err)
	}

	client.job.Spec.Template.Spec.InitContainers = []corev1.Container{{
		Name:  "unexpected",
		Image: "ghcr.io/example/unexpected@sha256:" + strings.Repeat("b", 64),
	}}
	if _, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate()); !errors.Is(err, ErrJobConflict) {
		t.Fatal("Job with an additional execution container was adopted", err)
	}
	client.job.Spec.Template.Spec.InitContainers = nil
	client.job.Annotations = nil
	if _, err := dispatcher.EnsureJob(context.Background(), testRunID, jobTemplate()); !errors.Is(err, ErrJobConflict) {
		t.Fatal("Job without control-plane identity was adopted", err)
	}
}

func TestEnsureJobValidation(t *testing.T) {
	valid := jobTemplate()
	badImage := jobTemplate()
	badImage.Spec.Template.Spec.Containers[0].Image = "ghcr.io/example/perfeng-k6:latest"
	retrying := jobTemplate()
	*retrying.Spec.BackoffLimit = 1
	automount := jobTemplate()
	*automount.Spec.Template.Spec.AutomountServiceAccountToken = true
	hostPath := jobTemplate()
	hostPath.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp"}},
	}}
	foreignIdentity := jobTemplate()
	foreignIdentity.Labels = map[string]string{runLabel: "perf-20260903-120000-87654321"}
	presetFingerprint := jobTemplate()
	presetFingerprint.Annotations = map[string]string{specAnnotation: strings.Repeat("a", 64)}

	for name, test := range map[string]struct {
		runID string
		job   *batchv1.Job
	}{
		"invalid-run":        {"invalid", valid},
		"nil-template":       {testRunID, nil},
		"generated-name":     {testRunID, func() *batchv1.Job { value := jobTemplate(); value.GenerateName = "run-"; return value }()},
		"mutable-image":      {testRunID, badImage},
		"automatic-retry":    {testRunID, retrying},
		"service-token":      {testRunID, automount},
		"host-path":          {testRunID, hostPath},
		"foreign-identity":   {testRunID, foreignIdentity},
		"preset-fingerprint": {testRunID, presetFingerprint},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestDispatcher(t, &memoryJobs{}).EnsureJob(
				context.Background(),
				test.runID,
				test.job,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatal("unsafe Job accepted", err)
			}
		})
	}
}

func TestEnsureJobPreservesContextAndRedactsAPIError(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestDispatcher(t, &memoryJobs{}).EnsureJob(
		cancelled,
		testRunID,
		jobTemplate(),
	); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity was lost", err)
	}

	client := &memoryJobs{createError: apierrors.NewForbidden(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"secret-job",
		errors.New("token=secret"),
	)}
	_, err := newTestDispatcher(t, client).EnsureJob(context.Background(), testRunID, jobTemplate())
	if err == nil || strings.Contains(err.Error(), "secret") || errors.Is(err, run.ErrUnavailable) {
		t.Fatal("unsafe Kubernetes API error classification", err)
	}
}

func TestNewDispatcherRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewDispatcher(nil, "perf-runs"); err == nil {
		t.Fatal("nil Kubernetes client accepted")
	}
	if _, err := NewDispatcher(&memoryJobs{}, "Invalid_Namespace"); err == nil {
		t.Fatal("invalid namespace accepted")
	}
}
