package integration

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// PublicationEventRequest supplies independently authenticated/configured scope
// and registered native evidence IDs. It supplies no event identity or clock.
type PublicationEventRequest struct {
	Provider              string
	Principal, Request    contract.CatalogIdentity
	ReportID, CandidateID string
}

// PublicationEventReservation is private coordination evidence, not publication
// authority. Publication still requires exact bytes and historical verification.
type PublicationEventReservation struct {
	EventID     string
	PublishedAt time.Time
}

// PublicationEventStore allocates stable event identities without external I/O.
// Implementations are concurrent-safe; errors return zero output and safe details.
type PublicationEventStore interface {
	// ReserveAnalysisPublicationEvent retains one identity/time per scoped request
	// and exact registered report/candidate. Identical retry/reopen recovers it;
	// changed evidence conflicts. Unknown/invisible requests return run.ErrNotFound.
	// An existing verified publication is authoritative and returns its original
	// event/time without retrospectively reserving a candidate. New reservation
	// requires native and reviewed verification evidence. Uncertain commits require
	// identical retry. This method never materializes or authorizes a publication.
	ReserveAnalysisPublicationEvent(context.Context, PublicationEventRequest) (PublicationEventReservation, error)
}
