package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
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

// ReportVerdictApprover validates or recomputes policy rule coverage and
// verdict arithmetic against the exact approved policy bytes.
type ReportVerdictApprover interface {
	// ApproveReportVerdicts accepts no report reference until its policy-driven
	// evaluations have been checked independently of the report producer.
	ApproveReportVerdicts(context.Context, []byte, analysisresult.Manifest) error
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

	references, err := approver.resolveReferences(ctx, principal, manifest.TestID, trust.Baselines)
	if err != nil {
		return err
	}
	if !sameArtifacts(manifest.ReferenceArtifacts, references) {
		return run.ErrValidation
	}

	return approver.verdicts.ApproveReportVerdicts(
		ctx,
		append([]byte(nil), trust.PolicyBytes...),
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
	principal, testID string,
	selections []baseline.Selection,
) ([]run.Artifact, error) {
	seenSelections := make(map[string]struct{}, len(selections))
	seenArtifacts := make(map[run.Artifact]struct{}, len(selections))
	references := make([]run.Artifact, 0, len(selections))
	for _, selection := range selections {
		key := selection.ID + "\x00" + selection.Version
		if selection.TestID != testID || selection.Validate() != nil {
			return nil, run.ErrValidation
		}
		if _, exists := seenSelections[key]; exists {
			return nil, run.ErrValidation
		}
		seenSelections[key] = struct{}{}

		record, found, err := approver.baselines.ResolveApprovedBaseline(ctx, principal, selection)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if !record.MatchesSelection(selection) || !validReportReference(record.Artifact) {
			return nil, run.ErrValidation
		}
		if _, exists := seenArtifacts[record.Artifact]; exists {
			continue
		}
		seenArtifacts[record.Artifact] = struct{}{}
		references = append(references, record.Artifact)
	}

	return references, nil
}

func validReportReference(artifact run.Artifact) bool {
	return artifact.Validate() == nil && artifact.Kind == "normalized" &&
		artifact.MediaType == "application/json" && artifact.Format == "normalized-result/v1"
}
