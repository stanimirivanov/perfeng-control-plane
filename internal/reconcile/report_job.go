package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const reportExecutable = "perfeng-report"

// ReportJobConfig supplies a reusable Pod specification and fixed container paths.
// Transfer containers and volumes remain deployment-owned template data.
type ReportJobConfig struct {
	Template              *batchv1.Job
	ContainerName         string
	CandidatePath         string
	CandidateArtifactPath string
	PolicyPath            string
	ReferenceDirectory    string
	OutputPath            string
}

// KubernetesReportJobResolver converts a trusted report plan into one restricted Job template.
type KubernetesReportJobResolver struct {
	plans  ReportPlanResolver
	config ReportJobConfig
}

var _ ReportJobResolver = (*KubernetesReportJobResolver)(nil)

// NewKubernetesReportJobResolver validates the reusable template and fixed file paths.
func NewKubernetesReportJobResolver(
	plans ReportPlanResolver,
	config ReportJobConfig,
) (*KubernetesReportJobResolver, error) {
	if plans == nil || !validReportJobConfig(config) {
		return nil, run.ErrValidation
	}

	config.Template = config.Template.DeepCopy()

	return &KubernetesReportJobResolver{plans: plans, config: config}, nil
}

// ResolveReportJob resolves trusted inputs and returns an independently owned Job.
func (resolver *KubernetesReportJobResolver) ResolveReportJob(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (*batchv1.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resolver == nil || resolver.plans == nil || !validReportJobConfig(resolver.config) ||
		principal == "" || current.State != run.StateReporting ||
		current.Request.Validate() != nil || !validReportCandidate(current.ID, input.Candidate) {
		return nil, run.ErrValidation
	}

	plan, err := resolver.plans.ResolveReportPlan(ctx, principal, current.Clone(), input)
	if err != nil {
		return nil, err
	}
	plan = plan.Clone()
	if plan.Validate(current.ID) != nil || plan.Candidate != input.Candidate ||
		!reportPlanMatchesRequest(plan, current.Request.Policy) {
		return nil, run.ErrValidation
	}

	template := resolver.config.Template.DeepCopy()
	container := jobContainer(template, resolver.config.ContainerName)
	if container == nil {
		return nil, run.ErrValidation
	}
	container.Image = plan.Producer.Image
	container.Command = []string{reportExecutable}
	container.Args = reportArguments(resolver.config, plan)
	if kubernetes.ValidateReusableJobTemplate(template) != nil {
		return nil, run.ErrValidation
	}

	return template, nil
}

func validReportJobConfig(config ReportJobConfig) bool {
	if config.ContainerName == "" || config.Template == nil ||
		kubernetes.ValidateReusableJobTemplate(config.Template) != nil {
		return false
	}
	paths := []string{
		config.CandidatePath,
		config.CandidateArtifactPath,
		config.PolicyPath,
		config.ReferenceDirectory,
		config.OutputPath,
	}
	if !uniqueStrings(paths) {
		return false
	}
	for _, value := range paths {
		if !validContainerPath(value) {
			return false
		}
	}

	container := jobContainer(config.Template, config.ContainerName)
	return container != nil && len(container.Command) == 0 && len(container.Args) == 0
}

func reportPlanMatchesRequest(plan ReportPlan, reference run.Reference) bool {
	document, err := policy.Parse(plan.PolicyBytes)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(plan.PolicyBytes)

	return document.Metadata.Name == reference.ID && document.Metadata.Version == reference.Version &&
		hex.EncodeToString(digest[:]) == reference.SHA256
}

func reportArguments(config ReportJobConfig, plan ReportPlan) []string {
	arguments := []string{
		"--candidate", config.CandidatePath,
		"--candidate-artifact", config.CandidateArtifactPath,
		"--policy", config.PolicyPath,
	}
	for _, resolution := range plan.Baselines {
		if resolution.Artifact == nil {
			continue
		}
		directory := path.Join(config.ReferenceDirectory, resolution.Artifact.ID)
		arguments = append(
			arguments,
			"--baseline", resolution.ID, resolution.Version,
			path.Join(directory, "normalized-result.json"),
			path.Join(directory, "artifact-reference.json"),
		)
	}

	return append(
		arguments,
		"--producer-image", plan.Producer.Image,
		"--output", config.OutputPath,
	)
}
