package reconcile

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// RawManifestResolver obtains the immutable reference published for one
// execution. The reference must come from trusted orchestration state, not from
// a caller-supplied location or an object-store listing.
type RawManifestResolver interface {
	ResolveRawManifest(context.Context, string, run.Run) (run.Artifact, error)
}

// RawManifestApprover checks parsed producer, test and workload claims against
// the accepted Run and its approved catalogue. It performs no object reads.
type RawManifestApprover interface {
	ApproveRawManifest(context.Context, string, run.Run, rawresult.Manifest) error
}

// ArtifactByteReader returns bytes only after enforcing storage policy and
// verifying the complete immutable artifact reference.
type ArtifactByteReader interface {
	Read(context.Context, run.Artifact) ([]byte, error)
}

// VerifiedRawCollector composes manifest resolution, byte verification,
// structural parsing and provenance approval for CollectionReconciler.
type VerifiedRawCollector struct {
	resolver         RawManifestResolver
	reader           ArtifactByteReader
	approver         RawManifestApprover
	contractsVersion string
}

var (
	_ RawArtifactCollector = (*VerifiedRawCollector)(nil)
	_ ArtifactByteReader   = (*objectstore.Reader)(nil)
)

// NewVerifiedRawCollector validates all collection dependencies and the exact
// contracts bundle version expected from the producer.
func NewVerifiedRawCollector(
	resolver RawManifestResolver,
	reader ArtifactByteReader,
	approver RawManifestApprover,
	contractsVersion string,
) (*VerifiedRawCollector, error) {
	if resolver == nil || reader == nil || approver == nil ||
		!rawresult.ValidContractsVersion(contractsVersion) {
		return nil, run.ErrValidation
	}

	return &VerifiedRawCollector{
		resolver:         resolver,
		reader:           reader,
		approver:         approver,
		contractsVersion: contractsVersion,
	}, nil
}

// CollectRawArtifacts verifies a complete evidence set without registering it.
// Missing objects remain retryable; invalid references, bytes, envelopes or
// provenance are classified as invalid execution output.
func (collector *VerifiedRawCollector) CollectRawArtifacts(
	ctx context.Context,
	principal string,
	current run.Run,
) (RawArtifactSet, error) {
	if err := ctx.Err(); err != nil {
		return RawArtifactSet{}, err
	}
	if principal == "" || !contract.ValidID(current.ID) {
		return RawArtifactSet{}, run.ErrValidation
	}

	manifestReference, err := collector.resolver.ResolveRawManifest(
		ctx,
		principal,
		current.Clone(),
	)
	if err != nil {
		return RawArtifactSet{}, classifyCollectionEvidence(err)
	}
	if !validRawManifestReference(manifestReference, current.ID) {
		return RawArtifactSet{}, ErrInvalidArtifacts
	}

	manifestBytes, err := collector.reader.Read(ctx, manifestReference)
	if err != nil {
		return RawArtifactSet{}, classifyCollectionEvidence(err)
	}
	manifest, err := rawresult.Parse(manifestBytes, current.ID, collector.contractsVersion)
	if err != nil {
		return RawArtifactSet{}, ErrInvalidArtifacts
	}
	collected := RawArtifactSet{
		Manifest:  manifestReference,
		Artifacts: append([]run.Artifact(nil), manifest.Artifacts...),
	}
	if collected.Validate(current.ID) != nil {
		return RawArtifactSet{}, ErrInvalidArtifacts
	}

	if err := collector.approver.ApproveRawManifest(
		ctx,
		principal,
		current.Clone(),
		cloneRawManifest(manifest),
	); err != nil {
		return RawArtifactSet{}, classifyCollectionApproval(err)
	}

	for _, artifact := range manifest.Artifacts {
		if _, err := collector.reader.Read(ctx, artifact); err != nil {
			return RawArtifactSet{}, classifyCollectionEvidence(err)
		}
	}

	return collected, nil
}

func validRawManifestReference(artifact run.Artifact, runID string) bool {
	return artifact.RunID == runID && artifact.Kind == "raw" &&
		artifact.MediaType == "application/json" && artifact.Format == "raw-result/v1" &&
		artifact.Validate() == nil
}

func classifyCollectionEvidence(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	ready := errors.Is(err, ErrArtifactsNotReady) || errors.Is(err, objectstore.ErrObjectNotFound)
	invalid := errors.Is(err, ErrInvalidArtifacts) ||
		errors.Is(err, objectstore.ErrObjectMismatch) || errors.Is(err, run.ErrValidation)
	if ready && invalid {
		return run.ErrValidation
	}
	if ready {
		return ErrArtifactsNotReady
	}
	if invalid {
		return ErrInvalidArtifacts
	}

	return err
}

func classifyCollectionApproval(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, run.ErrValidation) || errors.Is(err, run.ErrForbidden) ||
		errors.Is(err, ErrInvalidArtifacts) {
		return ErrInvalidArtifacts
	}

	return err
}

func cloneRawManifest(manifest rawresult.Manifest) rawresult.Manifest {
	manifest.Artifacts = append([]run.Artifact(nil), manifest.Artifacts...)
	return manifest
}
