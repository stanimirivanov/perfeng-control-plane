package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	reportExecutable                     = "perfeng-report"
	reportCandidateArtifactIDEnvironment = "PERFENG_REPORT_CANDIDATE_ARTIFACT_ID"
	reportCandidateRunIDEnvironment      = "PERFENG_REPORT_CANDIDATE_RUN_ID"
	reportCandidateKindEnvironment       = "PERFENG_REPORT_CANDIDATE_KIND"
	reportCandidateURIEnvironment        = "PERFENG_REPORT_CANDIDATE_URI"
	reportCandidateSHA256Environment     = "PERFENG_REPORT_CANDIDATE_SHA256"
	reportCandidateSizeEnvironment       = "PERFENG_REPORT_CANDIDATE_SIZE_BYTES"
	reportCandidateMediaTypeEnvironment  = "PERFENG_REPORT_CANDIDATE_MEDIA_TYPE"
	reportCandidateFormatEnvironment     = "PERFENG_REPORT_CANDIDATE_FORMAT"
	reportCandidatePathEnvironment       = "PERFENG_REPORT_CANDIDATE_PATH"
	reportCandidateReferenceEnvironment  = "PERFENG_REPORT_CANDIDATE_REFERENCE_PATH"
	reportPolicyIDEnvironment            = "PERFENG_REPORT_POLICY_ID"
	reportPolicyVersionEnvironment       = "PERFENG_REPORT_POLICY_VERSION"
	reportPolicySHA256Environment        = "PERFENG_REPORT_POLICY_SHA256"
	reportPolicyPathEnvironment          = "PERFENG_REPORT_POLICY_PATH"
)

// ReportJobConfig supplies a reusable Pod specification, the containers owned
// by report composition and their fixed file paths. The input container owns
// transfer mechanics and credentials; the resolver supplies artifact metadata.
type ReportJobConfig struct {
	Template              *batchv1.Job
	ContainerName         string
	InputContainerName    string
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
	inputContainer := jobInitContainer(template, resolver.config.InputContainerName)
	if container == nil || inputContainer == nil {
		return nil, run.ErrValidation
	}
	inputContainer.Env = append(
		inputContainer.Env,
		reportCandidateEnvironment(resolver.config, plan.Candidate)...,
	)
	inputContainer.Env = append(
		inputContainer.Env,
		reportPolicyEnvironment(resolver.config, current.Request.Policy)...,
	)
	container.Image = plan.Producer.Image
	container.Command = []string{reportExecutable}
	container.Args = reportArguments(resolver.config, plan)
	if kubernetes.ValidateReusableJobTemplate(template) != nil {
		return nil, run.ErrValidation
	}

	return template, nil
}

func validReportJobConfig(config ReportJobConfig) bool {
	if config.ContainerName == "" || config.InputContainerName == "" || config.Template == nil ||
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
	inputContainer := jobInitContainer(config.Template, config.InputContainerName)

	return container != nil && len(container.Command) == 0 && len(container.Args) == 0 &&
		inputContainer != nil && !hasReservedReportInputEnvironment(inputContainer.Env)
}

func reportCandidateEnvironment(config ReportJobConfig, candidate run.Artifact) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: reportCandidateArtifactIDEnvironment, Value: candidate.ID},
		{Name: reportCandidateRunIDEnvironment, Value: candidate.RunID},
		{Name: reportCandidateKindEnvironment, Value: candidate.Kind},
		{Name: reportCandidateURIEnvironment, Value: candidate.URI},
		{Name: reportCandidateSHA256Environment, Value: candidate.SHA256},
		{Name: reportCandidateSizeEnvironment, Value: strconv.FormatInt(candidate.SizeBytes, 10)},
		{Name: reportCandidateMediaTypeEnvironment, Value: candidate.MediaType},
		{Name: reportCandidateFormatEnvironment, Value: candidate.Format},
		{Name: reportCandidatePathEnvironment, Value: config.CandidatePath},
		{Name: reportCandidateReferenceEnvironment, Value: config.CandidateArtifactPath},
	}
}

func reportPolicyEnvironment(config ReportJobConfig, reference run.Reference) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: reportPolicyIDEnvironment, Value: reference.ID},
		{Name: reportPolicyVersionEnvironment, Value: reference.Version},
		{Name: reportPolicySHA256Environment, Value: reference.SHA256},
		{Name: reportPolicyPathEnvironment, Value: config.PolicyPath},
	}
}

func hasReservedReportInputEnvironment(environment []corev1.EnvVar) bool {
	for _, variable := range environment {
		switch variable.Name {
		case reportCandidateArtifactIDEnvironment,
			reportCandidateRunIDEnvironment,
			reportCandidateKindEnvironment,
			reportCandidateURIEnvironment,
			reportCandidateSHA256Environment,
			reportCandidateSizeEnvironment,
			reportCandidateMediaTypeEnvironment,
			reportCandidateFormatEnvironment,
			reportCandidatePathEnvironment,
			reportCandidateReferenceEnvironment,
			reportPolicyIDEnvironment,
			reportPolicyVersionEnvironment,
			reportPolicySHA256Environment,
			reportPolicyPathEnvironment:
			return true
		}
	}

	return false
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
