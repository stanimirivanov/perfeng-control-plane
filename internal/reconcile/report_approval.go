package reconcile

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportTrust contains the immutable policy and provenance selected from
// trusted registries for one reporting attempt.
type ReportTrust struct {
	PolicyBytes []byte
	PolicyMode  string
	Producer    rawresult.Producer
	Baselines   []baseline.Selection
}

// Clone returns report trust without shared mutable policy or baseline data.
func (trust ReportTrust) Clone() ReportTrust {
	trust.PolicyBytes = append([]byte(nil), trust.PolicyBytes...)
	trust.Baselines = append([]baseline.Selection(nil), trust.Baselines...)
	for index := range trust.Baselines {
		if trust.Baselines[index].Dataset.Seed != nil {
			seed := *trust.Baselines[index].Dataset.Seed
			trust.Baselines[index].Dataset.Seed = &seed
		}
	}

	return trust
}

// ReportTrustResolver resolves the exact run-pinned policy bytes, authorized
// report producer and policy-selected baseline compatibility dimensions.
type ReportTrustResolver interface {
	// ResolveReportTrust returns independently owned trust data derived from
	// approved registries, never from claims inside the report document.
	ResolveReportTrust(
		context.Context,
		string,
		run.Run,
		ReportingInput,
	) (ReportTrust, error)
}

// ReportVerdictApprover validates policy rule coverage, baseline-to-rule
// bindings and verdict arithmetic against exact approved inputs.
type ReportVerdictApprover interface {
	// ApproveReportVerdicts accepts no report reference until its policy-driven
	// evaluations and baseline bindings have been checked independently of the
	// report producer.
	ApproveReportVerdicts(
		ctx context.Context,
		policyBytes []byte,
		baselines []policy.BaselineResolution,
		manifest analysisresult.Manifest,
	) error
}

// TrustedReportApprover binds report claims to approved policy, producer and
// exact baseline versions before delegating verdict verification.
type TrustedReportApprover struct {
	plans    ReportPlanResolver
	verdicts ReportVerdictApprover
}

var _ ReportManifestApprover = (*TrustedReportApprover)(nil)

// NewTrustedReportApprover requires all trust and verification boundaries.
func NewTrustedReportApprover(
	baselines baseline.Repository,
	resolver ReportTrustResolver,
	verdicts ReportVerdictApprover,
) (*TrustedReportApprover, error) {
	if baselines == nil || resolver == nil || verdicts == nil {
		return nil, run.ErrValidation
	}
	plans, err := NewTrustedReportPlanResolver(baselines, resolver)
	if err != nil {
		return nil, run.ErrValidation
	}

	return &TrustedReportApprover{plans: plans, verdicts: verdicts}, nil
}

// ApproveReportManifest rejects policy, producer and reference claims that do
// not match independently resolved trust data.
func (approver *TrustedReportApprover) ApproveReportManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
	manifest analysisresult.Manifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if approver == nil || approver.plans == nil || approver.verdicts == nil ||
		principal == "" || current.State != run.StateReporting ||
		manifest.RunID != current.ID || manifest.TestID != current.Request.TestSuite ||
		manifest.CandidateArtifact != input.Candidate ||
		!validReportCandidate(current.ID, input.Candidate) {
		return run.ErrValidation
	}

	plan, err := approver.plans.ResolveReportPlan(
		ctx,
		principal,
		current.Clone(),
		input,
	)
	if err != nil {
		return err
	}
	plan = plan.Clone()
	if plan.Validate(current.ID) != nil || manifest.Policy.ID != current.Request.Policy.ID ||
		manifest.Policy.Version != current.Request.Policy.Version ||
		manifest.Policy.SHA256 != current.Request.Policy.SHA256 ||
		manifest.Policy.Mode != plan.PolicyMode || !manifest.Producer.Equal(plan.Producer) {
		return run.ErrValidation
	}
	if !sameArtifacts(manifest.ReferenceArtifacts, resolvedArtifacts(plan.Baselines)) {
		return run.ErrValidation
	}

	return approver.verdicts.ApproveReportVerdicts(
		ctx,
		append([]byte(nil), plan.PolicyBytes...),
		cloneBaselineResolutions(plan.Baselines),
		manifest,
	)
}

func matchesPolicyReferences(
	references []policy.Reference,
	selections []baseline.Selection,
	testID string,
) bool {
	if len(references) != len(selections) {
		return false
	}
	expected := make(map[policy.Reference]struct{}, len(references))
	for _, reference := range references {
		expected[reference] = struct{}{}
	}
	for _, selection := range selections {
		reference := policy.Reference{BaselineID: selection.ID, Version: selection.Version}
		if selection.TestID != testID || selection.Validate() != nil {
			return false
		}
		if _, exists := expected[reference]; !exists {
			return false
		}
		delete(expected, reference)
	}

	return len(expected) == 0
}

func resolvedArtifacts(resolutions []policy.BaselineResolution) []run.Artifact {
	artifacts := make([]run.Artifact, 0, len(resolutions))
	seen := make(map[run.Artifact]struct{}, len(resolutions))
	for _, resolution := range resolutions {
		if resolution.Artifact == nil {
			continue
		}
		artifact := *resolution.Artifact
		if _, exists := seen[artifact]; exists {
			continue
		}
		seen[artifact] = struct{}{}
		artifacts = append(artifacts, artifact)
	}

	return artifacts
}

func validReportReference(artifact run.Artifact) bool {
	return artifact.Validate() == nil && artifact.Kind == "normalized" &&
		artifact.MediaType == "application/json" && artifact.Format == "normalized-result/v1"
}
