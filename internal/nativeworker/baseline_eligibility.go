package nativeworker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BaselinePlanResolver returns the approved v2 plan for one accepted Run.
type BaselinePlanResolver interface {
	ResolveNativeBaselinePlan(context.Context, string, run.Run) (BaselinePlan, error)
}

// BaselineSelection is an advisory candidate and reviewed plan. Lease issuance
// must recheck both under storage locks; selection grants no worker authority.
type BaselineSelection struct {
	Index     int
	Candidate Candidate
	Plan      BaselinePlan
}

// SelectBaselineCandidate returns the first reviewed plan compatible with the
// v2 worker report. Runs with no v2 policy are skipped; resolver failures are
// not hidden as ineligibility.
func SelectBaselineCandidate(
	ctx context.Context,
	presence Presence,
	candidates []Candidate,
	resolver BaselinePlanResolver,
) (BaselineSelection, bool, error) {
	if resolver == nil || presence.ProtocolVersion != 2 || len(candidates) > maximumCandidateBatch {
		return BaselineSelection{}, false, run.ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return BaselineSelection{}, false, err
	}
	for index, candidate := range candidates {
		if candidate.Principal == "" || candidate.Run.State != run.StateCreated {
			return BaselineSelection{}, false, run.ErrValidation
		}
		plan, err := resolver.ResolveNativeBaselinePlan(ctx, candidate.Principal, candidate.Run)
		if errors.Is(err, run.ErrForbidden) {
			continue
		}
		if err != nil {
			return BaselineSelection{}, false, err
		}
		if presence.SupportsBaseline(plan) {
			return BaselineSelection{Index: index, Candidate: candidate, Plan: plan}, true, nil
		}
	}

	return BaselineSelection{}, false, nil
}

type baselineQualificationEvidence struct {
	EnvironmentID     string            `json:"environmentId"`
	EnvironmentSHA256 string            `json:"environmentSha256"`
	Runner            ExecutionArtifact `json:"runner"`
}

// SupportsBaseline reports whether one v2 presence observation can execute the
// complete reviewed baseline plan. The caller must independently establish
// authentication, qualification, authorization, freshness and lease capacity.
func (presence Presence) SupportsBaseline(plan BaselinePlan) bool {
	if presence.ProtocolVersion != 2 || presence.Availability != "AVAILABLE" ||
		!presence.Capabilities.validForRunnerContract(2) || plan.Validate() != nil {
		return false
	}
	capabilities := presence.Capabilities
	assignment := plan.Assignment
	if !contains(capabilities.Browsers, assignment.Browser) ||
		!contains(capabilities.DiagnosticModes, assignment.DiagnosticMode) ||
		!contains(capabilities.ExecutionArtifacts, assignment.ExecutionArtifact.Kind) {
		return false
	}
	for _, slot := range plan.ArtifactPlan {
		if !contains(capabilities.ArtifactKinds, slot.Kind) {
			return false
		}
	}

	var evidence baselineQualificationEvidence
	if err := json.Unmarshal(presence.QualificationEvidence, &evidence); err != nil {
		return false
	}

	return evidence.EnvironmentID == assignment.Recipe.Environment.ProfileID &&
		evidence.EnvironmentSHA256 == assignment.Recipe.Environment.EvidenceSHA256 &&
		evidence.Runner == assignment.ExecutionArtifact
}
