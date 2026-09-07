package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportPlan binds one candidate to its exact policy, report runtime and
// policy-selected baseline resolutions. A nil baseline artifact records that
// the exact approved reference is unavailable and must remain inconclusive.
type ReportPlan struct {
	Candidate   run.Artifact
	PolicyBytes []byte
	PolicyMode  string
	Producer    rawresult.Producer
	Baselines   []policy.BaselineResolution
}

// Clone returns a report plan without shared mutable policy or baseline data.
func (plan ReportPlan) Clone() ReportPlan {
	plan.PolicyBytes = append([]byte(nil), plan.PolicyBytes...)
	plan.Baselines = cloneBaselineResolutions(plan.Baselines)

	return plan
}

// Validate checks candidate, policy, runtime and baseline-reference coverage.
func (plan ReportPlan) Validate(runID string) error {
	document, err := policy.Parse(plan.PolicyBytes)
	if err != nil || !validReportCandidate(runID, plan.Candidate) ||
		plan.Producer.Validate() != nil || plan.PolicyMode != document.Spec.Mode ||
		!matchesBaselineResolutions(document.BaselineReferences(), plan.Baselines) {
		return run.ErrValidation
	}
	for _, resolution := range plan.Baselines {
		if resolution.Artifact != nil && !validReportReference(*resolution.Artifact) {
			return run.ErrValidation
		}
	}

	return nil
}

// ReportPlanResolver selects independently verified inputs for report execution.
type ReportPlanResolver interface {
	// ResolveReportPlan returns exact policy bytes, runtime, candidate and
	// approved baseline evidence. Missing exact baselines have nil artifacts;
	// registry, repository and context errors are preserved.
	ResolveReportPlan(context.Context, string, run.Run, ReportingInput) (ReportPlan, error)
}

// TrustedReportPlanResolver combines registry trust with approved baseline state.
type TrustedReportPlanResolver struct {
	baselines baseline.Repository
	trust     ReportTrustResolver
}

var _ ReportPlanResolver = (*TrustedReportPlanResolver)(nil)

// NewTrustedReportPlanResolver requires both registry and baseline trust boundaries.
func NewTrustedReportPlanResolver(
	baselines baseline.Repository,
	trust ReportTrustResolver,
) (*TrustedReportPlanResolver, error) {
	if baselines == nil || trust == nil {
		return nil, run.ErrValidation
	}

	return &TrustedReportPlanResolver{baselines: baselines, trust: trust}, nil
}

// ResolveReportPlan resolves and validates all policy-selected report inputs.
func (resolver *TrustedReportPlanResolver) ResolveReportPlan(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (ReportPlan, error) {
	if err := ctx.Err(); err != nil {
		return ReportPlan{}, err
	}
	if resolver == nil || resolver.baselines == nil || resolver.trust == nil ||
		principal == "" || current.State != run.StateReporting ||
		!validReportCandidate(current.ID, input.Candidate) {
		return ReportPlan{}, run.ErrValidation
	}

	trust, err := resolver.trust.ResolveReportTrust(ctx, principal, current.Clone(), input)
	if err != nil {
		return ReportPlan{}, err
	}
	trust = trust.Clone()
	document, err := policy.Parse(trust.PolicyBytes)
	if err != nil || !validReportTrust(current, trust) ||
		document.Metadata.Name != current.Request.Policy.ID ||
		document.Metadata.Version != current.Request.Policy.Version ||
		document.Spec.Mode != trust.PolicyMode ||
		!matchesPolicyReferences(document.BaselineReferences(), trust.Baselines, current.Request.TestSuite) {
		return ReportPlan{}, run.ErrValidation
	}

	resolutions, err := resolveReportReferences(ctx, resolver.baselines, principal, trust.Baselines)
	if err != nil {
		return ReportPlan{}, err
	}
	plan := ReportPlan{
		Candidate: input.Candidate, PolicyBytes: trust.PolicyBytes,
		PolicyMode: trust.PolicyMode, Producer: trust.Producer, Baselines: resolutions,
	}
	if plan.Validate(current.ID) != nil {
		return ReportPlan{}, run.ErrValidation
	}

	return plan.Clone(), nil
}

func validReportTrust(current run.Run, trust ReportTrust) bool {
	policyHash := sha256.Sum256(trust.PolicyBytes)
	return len(trust.PolicyBytes) > 0 &&
		hex.EncodeToString(policyHash[:]) == current.Request.Policy.SHA256 &&
		(trust.PolicyMode == "observe" || trust.PolicyMode == "inform") &&
		trust.Producer.Validate() == nil
}

func resolveReportReferences(
	ctx context.Context,
	repository baseline.Repository,
	principal string,
	selections []baseline.Selection,
) ([]policy.BaselineResolution, error) {
	resolutions := make([]policy.BaselineResolution, 0, len(selections))
	for _, selection := range selections {
		record, found, err := repository.ResolveApprovedBaseline(ctx, principal, selection)
		if err != nil {
			return nil, err
		}
		resolution := policy.BaselineResolution{ID: selection.ID, Version: selection.Version}
		if !found {
			resolutions = append(resolutions, resolution)
			continue
		}
		if !record.MatchesSelection(selection) || !validReportReference(record.Artifact) {
			return nil, run.ErrValidation
		}
		artifact := record.Artifact
		resolution.Artifact = &artifact
		resolutions = append(resolutions, resolution)
	}

	return resolutions, nil
}

func matchesBaselineResolutions(
	references []policy.Reference,
	resolutions []policy.BaselineResolution,
) bool {
	if len(references) != len(resolutions) {
		return false
	}
	expected := make(map[policy.Reference]struct{}, len(references))
	for _, reference := range references {
		expected[reference] = struct{}{}
	}
	for _, resolution := range resolutions {
		reference := policy.Reference{BaselineID: resolution.ID, Version: resolution.Version}
		if _, exists := expected[reference]; !exists {
			return false
		}
		delete(expected, reference)
	}

	return len(expected) == 0
}

func cloneBaselineResolutions(
	resolutions []policy.BaselineResolution,
) []policy.BaselineResolution {
	clone := append([]policy.BaselineResolution(nil), resolutions...)
	for index := range clone {
		if clone[index].Artifact == nil {
			continue
		}
		artifact := *clone[index].Artifact
		clone[index].Artifact = &artifact
	}

	return clone
}
