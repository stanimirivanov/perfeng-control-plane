package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// NormalizedManifestResolver obtains the immutable output reference published
// by a completed analysis execution. The reference must come from trusted
// orchestration state, not from a caller or an object-store listing.
type NormalizedManifestResolver interface {
	// ResolveNormalizedManifest returns the trusted publication reference or an
	// evidence-readiness error without listing arbitrary storage locations.
	ResolveNormalizedManifest(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error)
}

// NormalizedManifestApprover checks the parsed normalizer, workload, window and
// source claims against the accepted Run, analysis input and approved catalogue.
type NormalizedManifestApprover interface {
	// ApproveNormalizedManifest authorizes parsed provenance and source claims;
	// it performs no object reads or registration.
	ApproveNormalizedManifest(
		context.Context,
		string,
		run.Run,
		AnalysisInput,
		normalizedresult.Manifest,
	) error
}

// VerifiedNormalizedCollector composes trusted output resolution, byte
// verification, strict parsing, source binding and provenance approval.
type VerifiedNormalizedCollector struct {
	resolver         NormalizedManifestResolver
	reader           ArtifactByteReader
	approver         NormalizedManifestApprover
	contractsVersion string
}

var _ NormalizedArtifactCollector = (*VerifiedNormalizedCollector)(nil)

// NewVerifiedNormalizedCollector validates every adapter boundary and the exact
// contracts bundle version expected from the normalizer.
func NewVerifiedNormalizedCollector(
	resolver NormalizedManifestResolver,
	reader ArtifactByteReader,
	approver NormalizedManifestApprover,
	contractsVersion string,
) (*VerifiedNormalizedCollector, error) {
	if resolver == nil || reader == nil || approver == nil ||
		!rawresult.ValidContractsVersion(contractsVersion) {
		return nil, run.ErrValidation
	}

	return &VerifiedNormalizedCollector{
		resolver:         resolver,
		reader:           reader,
		approver:         approver,
		contractsVersion: contractsVersion,
	}, nil
}

// CollectNormalizedArtifact verifies and approves one normalized-result
// envelope without registering it. A missing publication remains retryable
// after Job success; malformed or contradictory evidence fails closed.
func (collector *VerifiedNormalizedCollector) CollectNormalizedArtifact(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if principal == "" || !contract.ValidID(current.ID) ||
		(RawArtifactSet{Manifest: input.Manifest, Artifacts: input.Sources}).Validate(current.ID) != nil {
		return run.Artifact{}, run.ErrValidation
	}

	reference, err := collector.resolver.ResolveNormalizedManifest(
		ctx,
		principal,
		current.Clone(),
		input.Clone(),
	)
	if err != nil {
		return run.Artifact{}, classifyAnalysisEvidence(err)
	}
	if validNormalizedArtifact(current.ID, reference, input) != nil {
		return run.Artifact{}, ErrAnalysisFailed
	}

	content, err := collector.reader.Read(ctx, reference)
	if err != nil {
		return run.Artifact{}, classifyAnalysisEvidence(err)
	}
	manifest, err := normalizedresult.Parse(content, current.ID, collector.contractsVersion)
	if err != nil || !sameArtifacts(manifest.SourceArtifacts, input.Sources) {
		return run.Artifact{}, ErrAnalysisFailed
	}
	rawManifest, err := collector.readRawManifest(ctx, current.ID, input)
	if err != nil {
		return run.Artifact{}, err
	}
	if !matchesRawProvenance(manifest, rawManifest) {
		return run.Artifact{}, ErrAnalysisFailed
	}

	if err := collector.approver.ApproveNormalizedManifest(
		ctx,
		principal,
		current.Clone(),
		input.Clone(),
		manifest,
	); err != nil {
		return run.Artifact{}, classifyAnalysisApproval(err)
	}

	return reference, nil
}

func (collector *VerifiedNormalizedCollector) readRawManifest(
	ctx context.Context,
	runID string,
	input AnalysisInput,
) (rawresult.Manifest, error) {
	content, err := collector.reader.Read(ctx, input.Manifest)
	if err != nil {
		return rawresult.Manifest{}, classifyPersistedAnalysisInput(err)
	}
	manifest, err := rawresult.Parse(content, runID, collector.contractsVersion)
	if err != nil || !sameArtifacts(manifest.Artifacts, input.Sources) {
		return rawresult.Manifest{}, ErrAnalysisFailed
	}

	return manifest, nil
}

func matchesRawProvenance(
	normalized normalizedresult.Manifest,
	raw rawresult.Manifest,
) bool {
	rawCreated, rawTimeErr := time.Parse(time.RFC3339Nano, raw.CreatedAt)
	normalizedCreated, normalizedTimeErr := time.Parse(time.RFC3339Nano, normalized.CreatedAt)
	return rawTimeErr == nil && normalizedTimeErr == nil &&
		!normalizedCreated.Before(rawCreated) &&
		normalized.ContractsVersion == raw.ContractsVersion &&
		normalized.TestID == raw.TestID && normalized.Workload == raw.Workload &&
		normalized.MeasurementWindow == raw.MeasurementWindow
}

func sameArtifacts(actual, expected []run.Artifact) bool {
	if len(actual) != len(expected) {
		return false
	}
	wanted := make(map[run.Artifact]struct{}, len(expected))
	for _, artifact := range expected {
		wanted[artifact] = struct{}{}
	}
	for _, artifact := range actual {
		if _, exists := wanted[artifact]; !exists {
			return false
		}
		delete(wanted, artifact)
	}

	return len(wanted) == 0
}

func classifyAnalysisEvidence(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	pending := errors.Is(err, ErrAnalysisPending) || errors.Is(err, objectstore.ErrObjectNotFound)
	invalid := errors.Is(err, ErrAnalysisFailed) || errors.Is(err, ErrInvalidArtifacts) ||
		errors.Is(err, objectstore.ErrObjectMismatch) || errors.Is(err, run.ErrValidation)
	if pending && invalid {
		return run.ErrValidation
	}
	if pending {
		return ErrAnalysisPending
	}
	if invalid {
		return ErrAnalysisFailed
	}

	return err
}

func classifyAnalysisApproval(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, run.ErrValidation) || errors.Is(err, run.ErrForbidden) ||
		errors.Is(err, ErrInvalidArtifacts) || errors.Is(err, ErrAnalysisFailed) {
		return ErrAnalysisFailed
	}

	return err
}

func classifyPersistedAnalysisInput(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, run.ErrUnavailable) {
		return err
	}
	if errors.Is(err, objectstore.ErrObjectNotFound) ||
		errors.Is(err, objectstore.ErrObjectMismatch) ||
		errors.Is(err, ErrInvalidArtifacts) || errors.Is(err, run.ErrValidation) {
		return ErrAnalysisFailed
	}

	return err
}
