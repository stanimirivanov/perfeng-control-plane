package app

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
)

const analysisPublicationVariable = "PERFENG_INTEGRATION_ANALYSIS_PUBLICATION_ENABLED"

// DurableReportPublicationStore combines historical materialization with private
// event reservations. Reservations do not replace publication verification.
type DurableReportPublicationStore interface {
	ReportPublicationStore
	integration.PublicationEventStore
}

// NewDurableReportingPublisher adapts durable allocation to the reporting gate.
// Construction performs no I/O. The verified reader is required for new writes;
// replay still bypasses allocation and object access. Dependencies must be
// concurrent-safe; apply migration 0023 and runtime grants before use.
func NewDurableReportingPublisher(store DurableReportPublicationStore, reader reconcile.ArtifactByteReader, provider, principalIssuer string) (*ReportingPublisher, error) {
	if store == nil || reader == nil {
		return nil, ErrInvalidConfiguration
	}
	resolve := func(ctx context.Context, input PublicationEvidenceRequest) (PublicationEvent, error) {
		if err := ctx.Err(); err != nil {
			return PublicationEvent{}, err
		}
		if input.EventID != "" || !input.PublishedAt.IsZero() {
			return PublicationEvent{}, ErrInvalidConfiguration
		}
		reservation, err := store.ReserveAnalysisPublicationEvent(ctx, integration.PublicationEventRequest{
			Provider: input.Provider, Principal: input.Principal, Request: input.Request,
			ReportID: input.ReportID, CandidateID: input.CandidateID})
		if err != nil {
			return PublicationEvent{}, reportingPublicationError(ctx, err)
		}
		if err := ctx.Err(); err != nil {
			return PublicationEvent{}, err
		}

		return PublicationEvent{ID: reservation.EventID, Time: reservation.PublishedAt}, nil
	}

	return NewReportingPublisher(store, reader, resolve, provider, principalIssuer)
}

func composeReportingPublisher(enabled bool, store ReconciliationStore, reader reconcile.ArtifactByteReader, namespaces integrationNamespaces) (reconcile.ReportPublisher, error) {
	if !enabled {
		return nil, nil
	}
	if namespaces == nil {
		return nil, ErrInvalidConfiguration
	}
	provider, principalIssuer, reviewed := namespaces.IntegrationNamespaces()
	durable, capable := store.(DurableReportPublicationStore)
	if !reviewed || !capable {
		return nil, ErrInvalidConfiguration
	}

	publisher, err := NewDurableReportingPublisher(durable, reader, provider, principalIssuer)
	if err != nil {
		return nil, err
	}

	return publisher, nil
}
