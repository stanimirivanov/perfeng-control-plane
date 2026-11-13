package app

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type rerunArtifactTrust struct {
	ArtifactTrust
	raw reconcile.RawManifestApprover
}

func (trust rerunArtifactTrust) ApproveRawManifest(ctx context.Context, principal string, current run.Run, manifest rawresult.Manifest) error {
	return trust.raw.ApproveRawManifest(ctx, principal, current, manifest)
}

func composeRerunArtifactTrust(enabled bool, database Database, trust ArtifactTrust) (ArtifactTrust, error) {
	if !enabled {
		return trust, nil
	}
	store, ok := database.(reconcile.RerunRawStore)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	review, ok := trust.(reconcile.RerunRawReview)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	raw, err := reconcile.NewRerunRawApprover(store, review)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return rerunArtifactTrust{ArtifactTrust: trust, raw: raw}, nil
}
