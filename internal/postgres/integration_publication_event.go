package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.PublicationEventStore = (*Repository)(nil)

var publicationArtifactID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func validPublicationEventRequest(input integration.PublicationEventRequest) bool {
	for _, value := range []string{input.Provider, input.Principal.Issuer, input.Principal.ID, input.Request.Issuer, input.Request.ID} {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return false
		}
	}

	return len(input.Provider) <= 512 && len(input.Principal.Issuer) <= 512 && len(input.Request.Issuer) <= 512 && len(input.Request.ID) <= 256 &&
		publicationArtifactID.MatchString(input.ReportID) && publicationArtifactID.MatchString(input.CandidateID) && input.ReportID != input.CandidateID
}

// ReserveAnalysisPublicationEvent serializes the same request key as admission
// and publication. Only new reservations allocate random identity and database time;
// replay never reviews current policy or reads objects. Commit failure is uncertain.
func (r *Repository) ReserveAnalysisPublicationEvent(ctx context.Context, input integration.PublicationEventRequest) (result integration.PublicationEventReservation, err error) {
	if err := ctx.Err(); err != nil {
		return integration.PublicationEventReservation{}, err
	}
	if !validPublicationEventRequest(input) {
		return integration.PublicationEventReservation{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return integration.PublicationEventReservation{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && err == nil {
			result, err = integration.PublicationEventReservation{}, storageError(rollbackErr)
		}
	}()
	key, err := json.Marshal([]string{"integration-request", input.Request.Issuer, input.Request.ID})
	if err != nil {
		return integration.PublicationEventReservation{}, run.ErrValidation
	}
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", scopeLock(input.Principal.ID, string(key))); err != nil {
		return integration.PublicationEventReservation{}, storageError(err)
	}
	record, err := readIntegration(ctx, tx, input.Principal.ID, input.Request)
	if err != nil {
		return integration.PublicationEventReservation{}, err
	}
	if record.Disposition.Source != input.Provider || record.Disposition.Data.Context.Principal != input.Principal {
		return integration.PublicationEventReservation{}, run.ErrNotFound
	}
	var original integration.PublicationEventReservation
	var provider, namespace, report, candidate string
	err = tx.QueryRowContext(ctx, `SELECT source,principal_issuer,report_id::text,candidate_id::text,event_id,published_at
		FROM perfeng_control.integration_publication_events WHERE principal=$1 AND issuer=$2 AND request_id=$3`, input.Principal.ID, input.Request.Issuer, input.Request.ID).
		Scan(&provider, &namespace, &report, &candidate, &original.EventID, &original.PublishedAt)
	if err != nil && !errors.Is(storageError(err), run.ErrNotFound) {
		return integration.PublicationEventReservation{}, storageError(err)
	}
	found := err == nil
	if found && (provider != input.Provider || namespace != input.Principal.Issuer || report != input.ReportID || candidate != input.CandidateID) {
		return integration.PublicationEventReservation{}, run.ErrConflict
	}
	raw, err := readAnalysisPublication(ctx, tx, input.Principal.ID, input.Request, record)
	if err == nil {
		var message contract.AnalysisPublication
		if json.Unmarshal(raw, &message) != nil {
			return integration.PublicationEventReservation{}, run.ErrUnavailable
		}
		published, err := time.Parse(time.RFC3339Nano, message.Time)
		if err != nil {
			return integration.PublicationEventReservation{}, run.ErrUnavailable
		}
		if message.Data.Artifact.ID != input.ReportID || (found && (original.EventID != message.ID || !original.PublishedAt.Equal(published))) {
			return integration.PublicationEventReservation{}, run.ErrConflict
		}

		return integration.PublicationEventReservation{EventID: message.ID, PublishedAt: published.UTC()}, nil
	}
	if !errors.Is(err, run.ErrNotFound) {
		return integration.PublicationEventReservation{}, err
	}
	if found {
		original.PublishedAt = original.PublishedAt.UTC()
		return original, nil
	}
	if record.Native == nil || record.Verification == nil {
		return integration.PublicationEventReservation{}, run.ErrConflict
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return integration.PublicationEventReservation{}, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return integration.PublicationEventReservation{}, run.ErrUnavailable
	}
	result = integration.PublicationEventReservation{EventID: "analysis-" + hex.EncodeToString(token[:]), PublishedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO perfeng_control.integration_publication_events
		(principal,issuer,request_id,source,principal_issuer,report_id,candidate_id,event_id,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		input.Principal.ID, input.Request.Issuer, input.Request.ID, input.Provider, input.Principal.Issuer, input.ReportID, input.CandidateID, result.EventID, result.PublishedAt)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23503" || pg.Code == "23505" || pg.Code == "23514") {
		return integration.PublicationEventReservation{}, run.ErrConflict
	}
	if err != nil {
		return integration.PublicationEventReservation{}, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return integration.PublicationEventReservation{}, storageError(err)
	}

	return result, nil
}
