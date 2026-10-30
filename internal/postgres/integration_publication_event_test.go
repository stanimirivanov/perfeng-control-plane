package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func publicationEventFixture(t *testing.T, r *Repository, suffix string, verified bool) (integration.PublicationEventRequest, contract.AnalysisPublication) {
	t.Helper()
	evidence, native := verificationFixture(t)
	message := integrationRequest(t, r, suffix)
	message.Data.Context.Baseline = nil
	record, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example", message,
		func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
			resolution := integration.AdmissionResolution{Request: native, Target: run.ExecutionTargetKubernetes,
				Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}
			if verified {
				resolution.Verification = &evidence
			}

			return resolution, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	publication := publicationFixtureFromAdmission(t, r, suffix, record)
	candidate := artifactFor(record.Run.ID)
	candidate.ID = "0" + publication.Data.Artifact.ID[1:]
	if candidate.ID == publication.Data.Artifact.ID {
		candidate.ID = "1" + publication.Data.Artifact.ID[1:]
	}
	candidate.Kind, candidate.Format, candidate.URI = "normalized", "normalized-result/v1", "s3://perfeng-artifacts/runs/"+record.Run.ID+"/candidate.json"
	if err := r.RegisterArtifact(testContext, "alice", candidate); err != nil {
		t.Fatal(err)
	}

	return integration.PublicationEventRequest{Provider: publication.Source, Principal: message.Data.Context.Principal, Request: message.Data.Context.Request,
		ReportID: publication.Data.Artifact.ID, CandidateID: candidate.ID}, publication
}

func TestPublicationEventOfflineValidation(t *testing.T) {
	input := integration.PublicationEventRequest{Provider: "https://provider.example", Principal: contract.CatalogIdentity{Issuer: "urn:principal", ID: "alice"},
		Request: contract.CatalogIdentity{Issuer: "urn:request", ID: "request"}, ReportID: "10000000-0000-4000-8000-000000000001", CandidateID: "10000000-0000-4000-8000-000000000002"}
	if !validPublicationEventRequest(input) {
		t.Fatal("valid scope rejected")
	}
	for field := range 7 {
		for _, bad := range []string{"", " ", "nul\x00value"} {
			changed := input
			fields := []*string{&changed.Provider, &changed.Principal.Issuer, &changed.Principal.ID, &changed.Request.Issuer, &changed.Request.ID, &changed.ReportID, &changed.CandidateID}
			*fields[field] = bad
			if got, err := (&Repository{}).ReserveAnalysisPublicationEvent(testContext, changed); !errors.Is(err, run.ErrValidation) || got != (integration.PublicationEventReservation{}) {
				t.Fatal("invalid input reached storage", err)
			}
		}
	}
	input.ReportID = input.CandidateID
	if validPublicationEventRequest(input) {
		t.Fatal("same report/candidate accepted")
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if got, err := (&Repository{}).ReserveAnalysisPublicationEvent(ctx, input); !errors.Is(err, context.Canceled) || got != (integration.PublicationEventReservation{}) {
		t.Fatal("cancellation lost", err)
	}
}

func TestPublicationEventConcurrentReplayReopenAndUpgrade(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	migrateBeforeVerification(t, r, "0023_")
	input, _ := publicationEventFixture(t, r, "event-upgrade", true)
	for range 2 {
		if err := r.Migrate(testContext); err != nil {
			t.Fatal(err)
		}
	}
	other := openTest(t, dsn)
	var wait sync.WaitGroup
	results := make(chan integration.PublicationEventReservation, 4)
	for _, store := range []*Repository{r, other, r, other} {
		wait.Go(func() {
			got, err := store.ReserveAnalysisPublicationEvent(testContext, input)
			if err != nil {
				t.Error(err)
				return
			}
			results <- got
		})
	}
	wait.Wait()
	close(results)
	var original integration.PublicationEventReservation
	for got := range results {
		if original.EventID == "" {
			original = got
		}
		if got != original || got.EventID == "" || got.PublishedAt.IsZero() {
			t.Fatal("allocation changed across concurrent calls")
		}
	}
	reopened := openTest(t, dsn)
	if got, err := reopened.ReserveAnalysisPublicationEvent(testContext, input); err != nil || got != original {
		t.Fatal("restart reassigned identity", err)
	}
	for _, field := range []string{"report", "candidate", "provider", "namespace", "owner", "request"} {
		changed, want := input, run.ErrConflict
		switch field {
		case "report":
			changed.ReportID = "99999999-9999-4999-8999-999999999999"
		case "candidate":
			changed.CandidateID = "99999999-9999-4999-8999-999999999999"
		case "provider":
			changed.Provider += "/other"
			want = run.ErrNotFound
		case "namespace":
			changed.Principal.Issuer += "/other"
			want = run.ErrNotFound
		case "owner":
			changed.Principal.ID = "bob"
			want = run.ErrNotFound
		case "request":
			changed.Request.ID += "-unknown"
			want = run.ErrNotFound
		}
		if got, err := r.ReserveAnalysisPublicationEvent(testContext, changed); !errors.Is(err, want) || got != (integration.PublicationEventReservation{}) {
			t.Fatal("conflicting/invisible reservation exposed", field, err)
		}
	}
	for _, statement := range []string{`UPDATE perfeng_control.integration_publication_events SET event_id='changed'`, `DELETE FROM perfeng_control.integration_publication_events`, `TRUNCATE perfeng_control.integration_publication_events CASCADE`} {
		_, err := r.db.Exec(statement)
		var blocked interface{ SQLState() string }
		if !errors.As(err, &blocked) || blocked.SQLState() != "55000" {
			t.Fatal("SQL mutation accepted", err)
		}
	}
}

func TestPublicationEventRejectsUnboundEvidenceAndRollsBack(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	input, _ := publicationEventFixture(t, r, "event-invalid", true)
	bad := input
	bad.ReportID = "99999999-9999-4999-8999-999999999999"
	if got, err := r.ReserveAnalysisPublicationEvent(testContext, bad); !errors.Is(err, run.ErrConflict) || got != (integration.PublicationEventReservation{}) {
		t.Fatal("unregistered report reserved", err)
	}
	if _, err := r.ReserveAnalysisPublicationEvent(testContext, input); err != nil {
		t.Fatal("failed insert leaked request reservation", err)
	}
	custom, _ := publicationEventFixture(t, r, "event-custom", false)
	if got, err := r.ReserveAnalysisPublicationEvent(testContext, custom); !errors.Is(err, run.ErrConflict) || got != (integration.PublicationEventReservation{}) {
		t.Fatal("unreviewed history reserved", err)
	}
	insert := `INSERT INTO perfeng_control.integration_publication_events(principal,issuer,request_id,source,principal_issuer,report_id,candidate_id,event_id,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,'forged',clock_timestamp())`
	_, err := r.db.Exec(insert,
		custom.Principal.ID, custom.Request.Issuer, custom.Request.ID, custom.Provider, custom.Principal.Issuer, custom.ReportID, custom.CandidateID)
	var blocked interface{ SQLState() string }
	if !errors.As(err, &blocked) || blocked.SQLState() != "23514" {
		t.Fatal("SQL bypassed reviewed admission", err)
	}
	for _, pair := range [][2]string{{input.CandidateID, input.ReportID}, {custom.ReportID, input.CandidateID}} {
		_, err := r.db.Exec(insert, input.Principal.ID, input.Request.Issuer, input.Request.ID, input.Provider, input.Principal.Issuer, pair[0], pair[1])
		if !errors.As(err, &blocked) || blocked.SQLState() != "23514" {
			t.Fatal("SQL bypassed artifact format/ownership", err)
		}
	}
}

func TestPublicationEventRecoversExistingPublicationWithoutReservation(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	input, message := publicationEventFixture(t, r, "event-published", false)
	verify := func(_ context.Context, _ integration.Record, proposed contract.AnalysisPublication) ([]byte, error) {
		return integrationcontract.EncodeAnalysisPublication(proposed)
	}
	if _, err := r.RetainAnalysisPublication(testContext, "alice", message, verify); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReserveAnalysisPublicationEvent(testContext, input)
	if err != nil || got.EventID != message.ID || got.PublishedAt.Format(time.RFC3339Nano) != message.Time {
		t.Fatal("existing publication allocated competing event", err)
	}
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.integration_publication_events`).Scan(&count); err != nil || count != 0 {
		t.Fatal("publication recovery backfilled reservation", err)
	}
}
