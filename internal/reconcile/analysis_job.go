package reconcile

import (
	"context"
	"path"
	"slices"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// AnalysisJobConfig supplies a reusable Pod specification and fixed container paths.
// Transfer containers and volumes remain deployment-owned template data;
// credential injection remains outside this resolver.
type AnalysisJobConfig struct {
	Template      *batchv1.Job
	ContainerName string
	ManifestPath  string
	SourcePath    string
	OutputPath    string
}

// KubernetesAnalysisJobResolver converts an approved plan into one restricted Job template.
type KubernetesAnalysisJobResolver struct {
	plans  AnalysisPlanResolver
	config AnalysisJobConfig
}

var _ AnalysisJobResolver = (*KubernetesAnalysisJobResolver)(nil)

// NewKubernetesAnalysisJobResolver validates the reusable template and fixed file paths.
func NewKubernetesAnalysisJobResolver(
	plans AnalysisPlanResolver,
	config AnalysisJobConfig,
) (*KubernetesAnalysisJobResolver, error) {
	if plans == nil || !validAnalysisJobConfig(config) {
		return nil, run.ErrValidation
	}

	config.Template = config.Template.DeepCopy()

	return &KubernetesAnalysisJobResolver{plans: plans, config: config}, nil
}

// ResolveAnalysisJob resolves trusted evidence and returns an independently owned Job.
func (resolver *KubernetesAnalysisJobResolver) ResolveAnalysisJob(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (*batchv1.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolver == nil || resolver.plans == nil || !validAnalysisJobConfig(resolver.config) ||
		principal == "" || current.State != run.StateAnalyzing ||
		current.Request.Validate() != nil ||
		(RawArtifactSet{Manifest: input.Manifest, Artifacts: input.Sources}).Validate(current.ID) != nil {
		return nil, run.ErrValidation
	}

	plan, err := resolver.plans.ResolveAnalysisPlan(
		ctx,
		principal,
		current.Clone(),
		input.Clone(),
	)
	if err != nil {
		return nil, err
	}
	if plan.Validate(current.ID) != nil || !slices.Contains(input.Sources, plan.Source) {
		return nil, run.ErrValidation
	}

	template := resolver.config.Template.DeepCopy()
	container := analysisContainer(template, resolver.config.ContainerName)
	if container == nil {
		return nil, run.ErrValidation
	}
	container.Image = plan.Producer.Image
	container.Args = normalizationArguments(resolver.config, plan)
	if kubernetes.ValidateReusableJobTemplate(template) != nil {
		return nil, run.ErrValidation
	}

	return template, nil
}

func validAnalysisJobConfig(config AnalysisJobConfig) bool {
	if config.ContainerName == "" || config.Template == nil ||
		kubernetes.ValidateReusableJobTemplate(config.Template) != nil {
		return false
	}
	paths := []string{config.ManifestPath, config.SourcePath, config.OutputPath}
	if !uniqueStrings(paths) {
		return false
	}
	for _, value := range paths {
		if !validContainerPath(value) {
			return false
		}
	}

	container := analysisContainer(config.Template, config.ContainerName)
	return container != nil && len(container.Command) == 0 && len(container.Args) == 0
}

func analysisContainer(template *batchv1.Job, name string) *corev1.Container {
	var match *corev1.Container
	for index := range template.Spec.Template.Spec.Containers {
		container := &template.Spec.Template.Spec.Containers[index]
		if container.Name != name {
			continue
		}
		if match != nil {
			return nil
		}
		match = container
	}

	return match
}

func normalizationArguments(config AnalysisJobConfig, plan AnalysisPlan) []string {
	arguments := []string{
		"--manifest", config.ManifestPath,
		"--summary", config.SourcePath,
		"--artifact-id", plan.Source.ID,
		"--producer-image", plan.Producer.Image,
	}
	if plan.Spec.SourceFormat == K6SummaryFormat {
		arguments = append(
			arguments,
			"--layout", plan.Spec.Layout,
			"--summary-covers-window",
		)
	}

	return append(arguments, "--output", config.OutputPath)
}

func validContainerPath(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value &&
		!strings.ContainsRune(value, '\x00')
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}

	return true
}
