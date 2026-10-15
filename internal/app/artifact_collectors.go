package app

import (
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
)

// ArtifactStorage combines deterministic publication discovery with verified
// immutable reads. S3ArtifactAccess satisfies this boundary.
type ArtifactStorage interface {
	reconcile.RawManifestResolver
	reconcile.NormalizedManifestResolver
	reconcile.ReportManifestResolver
	reconcile.ArtifactByteReader
}

// ArtifactTrust combines the immutable registry decisions used while attesting
// raw, normalized and report evidence. ReportPolicyRegistry satisfies it.
type ArtifactTrust interface {
	reconcile.ContractsVersionResolver
	reconcile.RawManifestApprover
	reconcile.NormalizedManifestApprover
	reconcile.ReportTrustResolver
}

// ArtifactCollectors contains the verified evidence boundary for each output stage.
type ArtifactCollectors struct {
	Raw        *reconcile.VerifiedRawCollector
	Normalized *reconcile.VerifiedNormalizedCollector
	Report     *reconcile.VerifiedReportCollector
}

// NewArtifactCollectors composes publication, byte, provenance, baseline and
// verdict checks without reading evidence or contacting an external service.
func NewArtifactCollectors(
	storage ArtifactStorage,
	trust ArtifactTrust,
	baselines baseline.Repository,
	config ArtifactVerificationConfig,
) (ArtifactCollectors, error) {
	if storage == nil || trust == nil || baselines == nil || config.Validate() != nil {
		return ArtifactCollectors{}, ErrInvalidConfiguration
	}

	contracts := artifactContracts{registry: trust, configured: config.ContractsVersion}
	raw, err := reconcile.NewVerifiedRawCollector(
		storage,
		storage,
		trust,
		contracts,
	)
	if err != nil {
		return ArtifactCollectors{}, ErrInvalidConfiguration
	}
	normalized, err := reconcile.NewVerifiedNormalizedCollector(
		storage,
		storage,
		trust,
		contracts,
	)
	if err != nil {
		return ArtifactCollectors{}, ErrInvalidConfiguration
	}
	reportApprover, err := reconcile.NewTrustedReportApprover(
		baselines,
		trust,
		policy.VerdictApprover{},
	)
	if err != nil {
		return ArtifactCollectors{}, ErrInvalidConfiguration
	}
	report, err := reconcile.NewVerifiedReportCollector(
		storage,
		storage,
		reportApprover,
		contracts,
	)
	if err != nil {
		return ArtifactCollectors{}, ErrInvalidConfiguration
	}

	return ArtifactCollectors{
		Raw:        raw,
		Normalized: normalized,
		Report:     report,
	}, nil
}
