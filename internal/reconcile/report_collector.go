package reconcile

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportManifestResolver obtains the immutable report reference published by a
// completed reporting execution. It must use trusted orchestration state rather
// than a caller-supplied location or object-store listing.
type ReportManifestResolver interface {
	// ResolveReportManifest returns the trusted publication reference or an
	// evidence-readiness error without listing arbitrary storage locations.
	ResolveReportManifest(context.Context, string, run.Run, ReportingInput) (run.Artifact, error)
}

// ReportManifestApprover authorizes the policy, selected references, producer
// and verdict claims against the accepted Run and trusted registries.
type ReportManifestApprover interface {
	// ApproveReportManifest authorizes parsed policy, reference and verdict claims;
	// it performs no object reads or registration.
	ApproveReportManifest(
		context.Context,
		string,
		run.Run,
		ReportingInput,
		analysisresult.Manifest,
	) error
}

// VerifiedReportCollector composes trusted output resolution, byte
// verification, strict parsing, candidate binding and report approval.
type VerifiedReportCollector struct {
	resolver         ReportManifestResolver
	reader           ArtifactByteReader
	approver         ReportManifestApprover
	contractsVersion string
}

var _ ReportArtifactCollector = (*VerifiedReportCollector)(nil)

// NewVerifiedReportCollector validates every report attestation boundary and
// the exact contracts bundle version expected from the report producer.
func NewVerifiedReportCollector(
	resolver ReportManifestResolver,
	reader ArtifactByteReader,
	approver ReportManifestApprover,
	contractsVersion string,
) (*VerifiedReportCollector, error) {
	if resolver == nil || reader == nil || approver == nil ||
		!rawresult.ValidContractsVersion(contractsVersion) {
		return nil, run.ErrValidation
	}

	return &VerifiedReportCollector{
		resolver: resolver, reader: reader, approver: approver,
		contractsVersion: contractsVersion,
	}, nil
}

// CollectReportArtifact verifies and approves one analysis-result report
// without registering it. Publication lag remains retryable after Job success.
func (collector *VerifiedReportCollector) CollectReportArtifact(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if principal == "" || !contract.ValidID(current.ID) ||
		!validReportCandidate(current.ID, input.Candidate) {
		return run.Artifact{}, run.ErrValidation
	}

	reference, err := collector.resolver.ResolveReportManifest(
		ctx,
		principal,
		current.Clone(),
		input,
	)
	if err != nil {
		return run.Artifact{}, classifyReportEvidence(err)
	}
	if validReportArtifact(current.ID, reference, []run.Artifact{input.Candidate}) != nil {
		return run.Artifact{}, ErrReportFailed
	}

	content, err := collector.reader.Read(ctx, reference)
	if err != nil {
		return run.Artifact{}, classifyReportEvidence(err)
	}
	manifest, err := analysisresult.Parse(content, current.ID, collector.contractsVersion)
	if err != nil || manifest.CandidateArtifact != input.Candidate {
		return run.Artifact{}, ErrReportFailed
	}

	if err := collector.approver.ApproveReportManifest(
		ctx,
		principal,
		current.Clone(),
		input,
		manifest,
	); err != nil {
		return run.Artifact{}, classifyReportApproval(err)
	}

	return reference, nil
}

func validReportCandidate(runID string, artifact run.Artifact) bool {
	return artifact.RunID == runID && artifact.Kind == "normalized" &&
		artifact.MediaType == "application/json" && artifact.Format == "normalized-result/v1" &&
		artifact.Validate() == nil
}

func classifyReportEvidence(err error) error {
	if operational := reportOperationalError(err); operational != nil {
		return operational
	}
	pending := errors.Is(err, ErrReportPending) || errors.Is(err, objectstore.ErrObjectNotFound)
	invalid := errors.Is(err, ErrReportFailed) || errors.Is(err, ErrInvalidArtifacts) ||
		errors.Is(err, objectstore.ErrObjectMismatch) || errors.Is(err, run.ErrValidation)
	if pending && invalid {
		return run.ErrValidation
	}
	if pending {
		return ErrReportPending
	}
	if invalid {
		return ErrReportFailed
	}

	return err
}

func classifyReportApproval(err error) error {
	if operational := reportOperationalError(err); operational != nil {
		return operational
	}
	if errors.Is(err, run.ErrValidation) || errors.Is(err, run.ErrForbidden) ||
		errors.Is(err, ErrInvalidArtifacts) || errors.Is(err, ErrReportFailed) {
		return ErrReportFailed
	}

	return err
}

func reportOperationalError(err error) error {
	for _, operational := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		run.ErrLeaseLost,
		run.ErrUnavailable,
		run.ErrArtifactConflict,
	} {
		if errors.Is(err, operational) {
			return operational
		}
	}

	return nil
}
