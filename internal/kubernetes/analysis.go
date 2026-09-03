package kubernetes

import (
	"context"
	"errors"

	batchv1 "k8s.io/api/batch/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	analysisJobSuffix = "-analysis"
	stageLabel        = "perfeng.io/stage"
	analysisStage     = "analysis"
)

// AnalysisDispatcher creates or adopts one deterministic normalization Job per Run.
// It reports Kubernetes process state without interpreting normalized output.
type AnalysisDispatcher struct {
	jobs      Jobs
	namespace string
}

// NewAnalysisDispatcher binds normalization Jobs to one namespace.
func NewAnalysisDispatcher(jobs Jobs, namespace string) (*AnalysisDispatcher, error) {
	if jobs == nil {
		return nil, errors.New("kubernetes Jobs client is required")
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) != 0 {
		return nil, errors.New("valid Kubernetes namespace is required")
	}

	return &AnalysisDispatcher{jobs: jobs, namespace: namespace}, nil
}

// EnsureAnalysisJob creates the normalization Job or adopts an identical owned
// Job after a retry, then returns its current process phase.
func (dispatcher *AnalysisDispatcher) EnsureAnalysisJob(
	ctx context.Context,
	runID string,
	template *batchv1.Job,
) (Observation, error) {
	desired, err := dispatcher.prepareAnalysisJob(runID, template)
	if err != nil {
		return Observation{}, err
	}

	actual, err := dispatcher.jobs.Create(ctx, desired, metav1.CreateOptions{})
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return Observation{}, classifyAPIError("create analysis Job", err)
		}
		actual, err = dispatcher.jobs.Get(ctx, desired.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return Observation{}, run.ErrUnavailable
			}
			return Observation{}, classifyAPIError("get analysis Job", err)
		}
	}
	if !matchingAnalysisJob(desired, actual) {
		return Observation{}, ErrJobConflict
	}

	return observe(actual), nil
}

func (dispatcher *AnalysisDispatcher) prepareAnalysisJob(
	runID string,
	template *batchv1.Job,
) (*batchv1.Job, error) {
	name := runID + analysisJobSuffix
	if !contract.ValidID(runID) || len(validation.IsDNS1123Label(name)) != 0 || template == nil {
		return nil, run.ErrValidation
	}
	if template.GenerateName != "" ||
		(template.Name != "" && template.Name != name) ||
		(template.Namespace != "" && template.Namespace != dispatcher.namespace) ||
		!analysisIdentityCompatible(template.Labels, runID) ||
		!analysisIdentityCompatible(template.Spec.Template.Labels, runID) ||
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
	desired.Name = name
	desired.Namespace = dispatcher.namespace
	desired.Labels = withAnalysisIdentity(desired.Labels, runID)
	desired.Spec.Template.Labels = withAnalysisIdentity(desired.Spec.Template.Labels, runID)
	desired.Annotations = withoutKey(desired.Annotations, specAnnotation)
	desired.Spec.Template.Annotations = withoutKey(desired.Spec.Template.Annotations, specAnnotation)
	fingerprint, err := jobFingerprint(desired)
	if err != nil {
		return nil, errors.New("could not fingerprint Kubernetes analysis Job specification")
	}
	desired.Annotations = withValue(desired.Annotations, specAnnotation, fingerprint)
	desired.Spec.Template.Annotations = withValue(desired.Spec.Template.Annotations, specAnnotation, fingerprint)

	return desired, nil
}

func matchingAnalysisJob(desired, actual *batchv1.Job) bool {
	if actual == nil || actual.UID == "" || actual.Name != desired.Name ||
		actual.Namespace != desired.Namespace || actual.Labels[runLabel] != desired.Labels[runLabel] ||
		actual.Labels[managedByLabel] != managedByValue || actual.Labels[stageLabel] != analysisStage ||
		actual.Annotations[specAnnotation] != desired.Annotations[specAnnotation] {
		return false
	}

	return apiequality.Semantic.DeepEqual(normalizedSpec(desired), normalizedSpec(actual))
}

func analysisIdentityCompatible(values map[string]string, runID string) bool {
	return identityCompatible(values, runID) &&
		(values[stageLabel] == "" || values[stageLabel] == analysisStage)
}

func withAnalysisIdentity(values map[string]string, runID string) map[string]string {
	return withValue(withIdentity(values, runID), stageLabel, analysisStage)
}
