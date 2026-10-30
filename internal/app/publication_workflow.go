package app

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// PublicationWorkflowStore combines scoped historical evidence and atomic
// publication retention, without current review, registration or listing access.
type PublicationWorkflowStore interface {
	PublicationEvidenceStore
	integrationcontract.MaterializationStore
}

// PublishRegisteredAnalysis coordinates first materialization and retained replay.
// Scope must be independently authenticated/configured; event ID/time and report
// ID must be stable on retry. Candidate ID and a configured verified reader are
// required only for new publication. Replay fetches no objects and performs no
// separate admission/artifact lookup or write in this layer. The materializer
// rereads the scoped ledger to keep canonical replay verification in one place.
// All failures return nil bytes; uncertain writes require identical retry.
func PublishRegisteredAnalysis(ctx context.Context, store PublicationWorkflowStore, reader reconcile.ArtifactByteReader, input PublicationEvidenceRequest) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, ErrInvalidConfiguration
	}
	if input.Provider == "" || input.Principal.ID == "" || input.Principal.Issuer == "" || input.Request.ID == "" || input.Request.Issuer == "" ||
		input.EventID == "" || input.PublishedAt.IsZero() || input.ReportID == "" {
		return nil, contract.ErrAnalysisPublication
	}
	raw, err := store.GetAnalysisPublication(ctx, input.Principal.ID, input.Request)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil {
		message, err := integrationcontract.DecodeAnalysisPublication(raw)
		if err != nil {
			return nil, run.ErrUnavailable
		}
		if message.Source != input.Provider || message.Data.Context.Principal != input.Principal || message.Data.Context.Request != input.Request {
			return nil, run.ErrForbidden
		}
		if message.Data.Artifact.ID != input.ReportID {
			return nil, run.ErrConflict
		}
		claim := message.Data.Artifact
		size, err := claim.SizeBytes.Int64()
		if err != nil {
			return nil, run.ErrUnavailable
		}
		// Only retained metadata supplies the native reference; opaque public Run
		// identity never selects a path and caller URLs are not accepted.
		report := run.Artifact{ID: claim.ID, RunID: claim.RunID, Kind: "normalized", URI: claim.URI,
			SHA256: claim.SHA256, SizeBytes: size, MediaType: claim.MediaType, Format: claim.Format}

		return integrationcontract.MaterializeAnalysisPublication(ctx, store, integrationcontract.MaterializationInput{
			Provider: input.Provider, Principal: input.Principal, Request: input.Request, EventID: input.EventID,
			PublishedAt: input.PublishedAt, Report: report})
	}
	if !errors.Is(err, run.ErrNotFound) {
		return nil, publicationEvidenceError(ctx, err)
	}
	prepared, err := PrepareAnalysisPublication(ctx, store, reader, input)
	if err != nil {
		return nil, err
	}

	return integrationcontract.MaterializeAnalysisPublication(ctx, store, prepared)
}
