package reconcile

import (
	"context"
	"path"
	"slices"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	manifestArtifactIDEnvironment = "PERFENG_RAW_MANIFEST_ARTIFACT_ID"
	manifestURIEnvironment        = "PERFENG_RAW_MANIFEST_URI"
	manifestSHA256Environment     = "PERFENG_RAW_MANIFEST_SHA256"
	manifestSizeEnvironment       = "PERFENG_RAW_MANIFEST_SIZE_BYTES"
	manifestPathEnvironment       = "PERFENG_RAW_MANIFEST_PATH"
	sourceArtifactIDEnvironment   = "PERFENG_SOURCE_ARTIFACT_ID"
	sourceURIEnvironment          = "PERFENG_SOURCE_URI"
	sourceSHA256Environment       = "PERFENG_SOURCE_SHA256"
	sourceSizeEnvironment         = "PERFENG_SOURCE_SIZE_BYTES"
	sourcePathEnvironment         = "PERFENG_SOURCE_PATH"
)

// AnalysisJobConfig supplies a reusable Pod specification, the containers
// owned by analysis composition and their fixed file paths. The input container
// owns transfer mechanics and credentials; the resolver supplies artifact
// metadata only.
type AnalysisJobConfig struct {
	Template           *batchv1.Job
	ContainerName      string
	InputContainerName string
	ManifestPath       string
	SourcePath         string
	OutputPath         string
}

// Validate checks the reusable template, owned containers and fixed distinct
// workspace paths without modifying the Job.
func (config AnalysisJobConfig) Validate() error {
	if !validAnalysisJobConfig(config) {
		return run.ErrValidation
	}

	return nil
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
	if plans == nil || config.Validate() != nil {
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
	container := jobContainer(template, resolver.config.ContainerName)
	inputContainer := jobInitContainer(template, resolver.config.InputContainerName)
	if container == nil || inputContainer == nil {
		return nil, run.ErrValidation
	}
	inputContainer.Env = append(
		inputContainer.Env,
		analysisInputEnvironment(resolver.config, input.Manifest, plan.Source)...,
	)
	container.Image = plan.Producer.Image
	container.Args = normalizationArguments(resolver.config, plan)
	if kubernetes.ValidateReusableJobTemplate(template) != nil {
		return nil, run.ErrValidation
	}

	return template, nil
}

func validAnalysisJobConfig(config AnalysisJobConfig) bool {
	if config.ContainerName == "" || config.InputContainerName == "" || config.Template == nil ||
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

	container := jobContainer(config.Template, config.ContainerName)
	inputContainer := jobInitContainer(config.Template, config.InputContainerName)

	return container != nil && len(container.Command) == 0 && len(container.Args) == 0 &&
		inputContainer != nil && !hasReservedAnalysisInputEnvironment(inputContainer.Env)
}

func jobContainer(template *batchv1.Job, name string) *corev1.Container {
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

func jobInitContainer(template *batchv1.Job, name string) *corev1.Container {
	var match *corev1.Container
	for index := range template.Spec.Template.Spec.InitContainers {
		container := &template.Spec.Template.Spec.InitContainers[index]
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

func analysisInputEnvironment(
	config AnalysisJobConfig,
	manifest run.Artifact,
	source run.Artifact,
) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: manifestArtifactIDEnvironment, Value: manifest.ID},
		{Name: manifestURIEnvironment, Value: manifest.URI},
		{Name: manifestSHA256Environment, Value: manifest.SHA256},
		{Name: manifestSizeEnvironment, Value: strconv.FormatInt(manifest.SizeBytes, 10)},
		{Name: manifestPathEnvironment, Value: config.ManifestPath},
		{Name: sourceArtifactIDEnvironment, Value: source.ID},
		{Name: sourceURIEnvironment, Value: source.URI},
		{Name: sourceSHA256Environment, Value: source.SHA256},
		{Name: sourceSizeEnvironment, Value: strconv.FormatInt(source.SizeBytes, 10)},
		{Name: sourcePathEnvironment, Value: config.SourcePath},
	}
}

func hasReservedAnalysisInputEnvironment(environment []corev1.EnvVar) bool {
	for _, variable := range environment {
		switch variable.Name {
		case manifestArtifactIDEnvironment,
			manifestURIEnvironment,
			manifestSHA256Environment,
			manifestSizeEnvironment,
			manifestPathEnvironment,
			sourceArtifactIDEnvironment,
			sourceURIEnvironment,
			sourceSHA256Environment,
			sourceSizeEnvironment,
			sourcePathEnvironment:
			return true
		}
	}

	return false
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
