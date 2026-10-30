package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// PublicationEvidenceStore exposes owned scoped history and registered references
// only; it grants no registration, listing, current review or publication writes.
type PublicationEvidenceStore interface {
	// GetIntegration returns owned retained evidence or scoped not-found.
	GetIntegration(context.Context, string, contract.CatalogIdentity) (integration.Record, error)
	// GetArtifact returns a reference belonging to the explicit principal and Run.
	GetArtifact(context.Context, string, string, string) (run.Artifact, error)
}

// PublicationEvidenceRequest carries independently authenticated/configured scope,
// stable event identity/time and canonical registered artifact IDs. It contains
// no caller-supplied URL, native Run ID, policy bytes or producer authority.
type PublicationEvidenceRequest struct {
	Provider              string
	Principal, Request    contract.CatalogIdentity
	EventID               string
	PublishedAt           time.Time
	ReportID, CandidateID string
}

// PrepareAnalysisPublication prepares evidence for NEW materialization, performing
// scoped reference reads then one bounded verified report read outside transactions.
// Use the configured objectstore reader; it enforces bucket/Run location, metadata,
// size/hash and body ownership. Candidate bytes are never fetched. Materialization
// still validates report schema/provenance/outcomes and transaction-supplied history.
// Retained replay must bypass preparation and object I/O. Failures return zero
// output and classified/redacted errors; success owns its exact report bytes.
func PrepareAnalysisPublication(ctx context.Context, store PublicationEvidenceStore, reader reconcile.ArtifactByteReader, input PublicationEvidenceRequest) (integrationcontract.MaterializationInput, error) {
	fail := func(err error) (integrationcontract.MaterializationInput, error) {
		return integrationcontract.MaterializationInput{}, publicationEvidenceError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if store == nil || reader == nil {
		return fail(ErrInvalidConfiguration)
	}
	if input.Provider == "" || input.Principal.ID == "" || input.Principal.Issuer == "" || input.Request.ID == "" || input.Request.Issuer == "" ||
		input.EventID == "" || input.PublishedAt.IsZero() || input.ReportID == "" || input.CandidateID == "" || input.ReportID == input.CandidateID {
		return fail(contract.ErrAnalysisPublication)
	}
	record, err := store.GetIntegration(ctx, input.Principal.ID, input.Request)
	if err != nil {
		return fail(err)
	}
	if record.Disposition.Source != input.Provider || record.Disposition.Data.Context.Principal != input.Principal || record.Disposition.Data.Context.Request != input.Request {
		return fail(run.ErrForbidden)
	}
	if record.Run == nil || record.Native == nil || record.Native.RunID != record.Run.ID || record.Verification == nil ||
		record.Disposition.Type != "dev.perfeng.integration.accepted.v1" || record.Disposition.Data.Run == nil ||
		record.Disposition.Data.Run.Issuer != input.Provider || record.Disposition.Data.Run.ID == "" || record.Verification.Validate(record.Run.Request) != nil {
		return fail(contract.ErrAnalysisPublication)
	}
	report, err := store.GetArtifact(ctx, input.Principal.ID, record.Native.RunID, input.ReportID)
	if err != nil {
		return fail(err)
	}
	if report.Validate() != nil || report.ID != input.ReportID || report.RunID != record.Native.RunID || report.Kind != "normalized" ||
		report.Format != "analysis-result/v1" || report.MediaType != "application/json" || report.SizeBytes < 1 || report.SizeBytes > 1<<20 {
		return fail(contract.ErrAnalysisPublication)
	}
	candidate, err := store.GetArtifact(ctx, input.Principal.ID, record.Native.RunID, input.CandidateID)
	if err != nil {
		return fail(err)
	}
	if candidate.Validate() != nil || candidate.ID != input.CandidateID || candidate.RunID != record.Native.RunID || candidate.Kind != "normalized" ||
		candidate.Format != "normalized-result/v1" || candidate.MediaType != "application/json" || candidate.SizeBytes < 1 || candidate.URI == report.URI {
		return fail(contract.ErrAnalysisPublication)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	raw, err := reader.Read(ctx, report)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if int64(len(raw)) != report.SizeBytes {
		return fail(contract.ErrAnalysisPublication)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != report.SHA256 {
		return fail(contract.ErrAnalysisPublication)
	}

	return integrationcontract.MaterializationInput{Provider: input.Provider, Principal: input.Principal, Request: input.Request,
		EventID: input.EventID, PublishedAt: input.PublishedAt, Report: report, Candidate: candidate, ReportBytes: bytes.Clone(raw)}, nil
}

func publicationEvidenceError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, classified := range []error{context.Canceled, context.DeadlineExceeded, run.ErrForbidden, run.ErrNotFound, contract.ErrAnalysisPublication, ErrInvalidConfiguration} {
		if errors.Is(err, classified) {
			return classified
		}
	}
	if errors.Is(err, objectstore.ErrObjectNotFound) {
		return run.ErrNotFound
	}
	if errors.Is(err, run.ErrValidation) || errors.Is(err, objectstore.ErrObjectMismatch) {
		return contract.ErrAnalysisPublication
	}

	return run.ErrUnavailable
}
