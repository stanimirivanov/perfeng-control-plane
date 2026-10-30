package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportPublicationStore supplies scoped native admission and historical publication
// capabilities. It grants no current review, artifact registration or listing.
type ReportPublicationStore interface {
	integration.RunAdmissionReader
	PublicationWorkflowStore
}

// PublicationEvent is a stable retained event identity, never a fresh retry clock.
type PublicationEvent struct {
	ID   string
	Time time.Time
}

// PublicationEventResolver is trusted in-process identity allocation/recovery.
// Input contains independently established scope and registered artifact IDs, with
// empty EventID/PublishedAt. Implementations must be concurrent-safe, durably retain
// identical event/time across retries/restarts and reject differing evidence for
// an existing reservation. They must honor ctx and return classified safe errors.
// An ephemeral clock/random callback is unsafe for runtime activation.
type PublicationEventResolver func(context.Context, PublicationEvidenceRequest) (PublicationEvent, error)

// ReportingPublisher connects the reporting gate to scoped historical publication.
// Construct it explicitly; no runtime opts in automatically. Calls are concurrent-
// safe when dependencies are, and never reconstruct missing historical authority.
type ReportingPublisher struct {
	store                     ReportPublicationStore
	reader                    reconcile.ArtifactByteReader
	resolve                   PublicationEventResolver
	provider, principalIssuer string
}

var _ reconcile.ReportPublisher = (*ReportingPublisher)(nil)

// NewReportingPublisher requires configured namespaces, store and trusted durable
// event resolver. A nil reader supports retained replay only; new publication
// requires the configured verified reader. It performs no I/O or activation.
func NewReportingPublisher(store ReportPublicationStore, reader reconcile.ArtifactByteReader, resolve PublicationEventResolver, provider, principalIssuer string) (*ReportingPublisher, error) {
	if store == nil || resolve == nil {
		return nil, ErrInvalidConfiguration
	}
	for _, issuer := range []string{provider, principalIssuer} {
		parsed, err := url.Parse(issuer)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(issuer) > 512 {
			return nil, ErrInvalidConfiguration
		}
	}

	return &ReportingPublisher{store: store, reader: reader, resolve: resolve, provider: provider, principalIssuer: principalIssuer}, nil
}

// PublishReport skips native-only and unsupported historical admissions, and
// otherwise requires exact retained scope/native request. Retained replay precedes
// missing-verification checks, event resolution and object access. New publication
// delegates all evidence/transaction verification to the existing workflow.
func (publisher *ReportingPublisher) PublishReport(ctx context.Context, principal string, current run.Run, candidate reconcile.ReportingInput, report run.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if publisher == nil || publisher.store == nil {
		return ErrInvalidConfiguration
	}
	if strings.TrimSpace(principal) == "" || strings.ContainsRune(principal, 0) || !contract.ValidID(current.ID) || current.Request.Validate() != nil ||
		candidate.Candidate.Validate() != nil || report.Validate() != nil || candidate.Candidate.RunID != current.ID || report.RunID != current.ID ||
		candidate.Candidate.ID == report.ID || candidate.Candidate.Format != "normalized-result/v1" || report.Format != "analysis-result/v1" ||
		candidate.Candidate.Kind != "normalized" || report.Kind != "normalized" || candidate.Candidate.MediaType != "application/json" || report.MediaType != "application/json" {
		return contract.ErrAnalysisPublication
	}
	scope := integration.RunAdmissionScope{Provider: publisher.provider, Principal: contract.CatalogIdentity{Issuer: publisher.principalIssuer, ID: principal}, RunID: current.ID}
	record, err := publisher.store.GetIntegrationForRun(ctx, scope)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, run.ErrNotFound) {
		return nil
	}
	if err != nil {
		return reportingPublicationError(ctx, err)
	}
	if record.Run == nil || record.Run.ID != current.ID || record.Run.Request != current.Request ||
		record.Disposition.Source != scope.Provider || record.Disposition.Data.Context.Principal != scope.Principal ||
		record.Disposition.Type != "dev.perfeng.integration.accepted.v1" || record.Disposition.Data.Context.Request.ID == "" || record.Disposition.Data.Context.Request.Issuer == "" {
		return contract.ErrAnalysisPublication
	}
	input := PublicationEvidenceRequest{Provider: scope.Provider, Principal: scope.Principal, Request: record.Disposition.Data.Context.Request,
		ReportID: report.ID, CandidateID: candidate.Candidate.ID}
	raw, err := publisher.store.GetAnalysisPublication(ctx, principal, input.Request)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		message, decodeErr := integrationcontract.DecodeAnalysisPublication(raw)
		if decodeErr != nil {
			return run.ErrUnavailable
		}
		if message.Source != scope.Provider || message.Data.Context.Principal != scope.Principal || message.Data.Context.Request != input.Request || message.Data.Native.RunID != current.ID {
			return run.ErrForbidden
		}
		input.EventID = message.ID
		input.PublishedAt, err = time.Parse(time.RFC3339Nano, message.Time)
		if err != nil {
			return run.ErrUnavailable
		}
	} else {
		if !errors.Is(err, run.ErrNotFound) {
			return reportingPublicationError(ctx, err)
		}
		if (record.Native != nil && record.Native.RunID != current.ID) || (record.Verification != nil && record.Verification.Validate(current.Request) != nil) {
			return contract.ErrAnalysisPublication
		}
		if record.Native == nil || record.Verification == nil {
			return nil
		}
		event, err := publisher.resolve(ctx, input)
		if err != nil {
			return reportingPublicationError(ctx, err)
		}
		input.EventID, input.PublishedAt = event.ID, event.Time
	}
	if input.EventID == "" || len(input.EventID) > 256 || strings.ContainsRune(input.EventID, 0) || input.PublishedAt.IsZero() {
		return contract.ErrAnalysisPublication
	}
	_, err = PublishRegisteredAnalysis(ctx, publisher.store, publisher.reader, input)

	return err
}

func reportingPublicationError(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		for _, classified := range []error{run.ErrConflict, run.ErrLeaseLost, run.ErrRevision} {
			if errors.Is(err, classified) {
				return classified
			}
		}
	}

	return publicationEvidenceError(ctx, err)
}
