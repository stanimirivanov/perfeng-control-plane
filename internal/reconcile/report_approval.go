package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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
	baselines baseline.Repository
	resolver  ReportTrustResolver
	verdicts  ReportVerdictApprover
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

	return &TrustedReportApprover{baselines: baselines, resolver: resolver, verdicts: verdicts}, nil
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
	if principal == "" || current.State != run.StateReporting ||
		manifest.RunID != current.ID || manifest.TestID != current.Request.TestSuite ||
		manifest.CandidateArtifact != input.Candidate ||
		!validReportCandidate(current.ID, input.Candidate) {
		return run.ErrValidation
	}

	trust, err := approver.resolver.ResolveReportTrust(
		ctx,
		principal,
		current.Clone(),
		input,
	)
	if err != nil {
		return err
	}
	trust = trust.Clone()
	if !validReportTrust(current, manifest, trust) {
		return run.ErrValidation
	}
	document, err := policy.Parse(trust.PolicyBytes)
	if err != nil || document.Metadata.Name != current.Request.Policy.ID ||
		document.Metadata.Version != current.Request.Policy.Version ||
		document.Spec.Mode != trust.PolicyMode ||
		!matchesPolicyReferences(document.BaselineReferences(), trust.Baselines, manifest.TestID) {
		return run.ErrValidation
	}

	resolutions, err := approver.resolveReferences(ctx, principal, trust.Baselines)
	if err != nil {
		return err
	}
	if !sameArtifacts(manifest.ReferenceArtifacts, resolvedArtifacts(resolutions)) {
		return run.ErrValidation
	}

	return approver.verdicts.ApproveReportVerdicts(
		ctx,
		append([]byte(nil), trust.PolicyBytes...),
		resolutions,
		manifest,
	)
}

func validReportTrust(
	current run.Run,
	manifest analysisresult.Manifest,
	trust ReportTrust,
) bool {
	policyHash := sha256.Sum256(trust.PolicyBytes)
	requestPolicy := current.Request.Policy
	return len(trust.PolicyBytes) > 0 &&
		hex.EncodeToString(policyHash[:]) == requestPolicy.SHA256 &&
		manifest.Policy.ID == requestPolicy.ID &&
		manifest.Policy.Version == requestPolicy.Version &&
		manifest.Policy.SHA256 == requestPolicy.SHA256 &&
		manifest.Policy.Mode == trust.PolicyMode &&
		(trust.PolicyMode == "observe" || trust.PolicyMode == "inform") &&
		trust.Producer.Validate() == nil && manifest.Producer == trust.Producer
}

func (approver *TrustedReportApprover) resolveReferences(
	ctx context.Context,
	principal string,
	selections []baseline.Selection,
) ([]policy.BaselineResolution, error) {
	resolutions := make([]policy.BaselineResolution, 0, len(selections))
	for _, selection := range selections {
		record, found, err := approver.baselines.ResolveApprovedBaseline(ctx, principal, selection)
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
