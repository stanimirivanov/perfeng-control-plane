package integrationcontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// MaterializationStore supplies authenticated scoped history and atomic retention.
// Implementations obey the ownership/concurrency contracts of the integration ports.
type MaterializationStore interface {
	integration.PublicationRepository
	// GetIntegration returns owned history or scoped not-found; it never reviews
	// current approval or reconstructs missing historical verification.
	GetIntegration(context.Context, string, contract.CatalogIdentity) (integration.Record, error)
}

// MaterializationInput contains trusted configured/authenticated namespaces, a
// scoped request and stable event identity/time. Report/Candidate must be registered
// references; callers enforce storage origin/allowlists and artifact authenticity
// before supplying exact ReportBytes. No policy or producer authority comes from
// this input. Do not mutate inputs concurrently. Replay needs only report metadata.
type MaterializationInput struct {
	Provider           string
	Principal, Request contract.CatalogIdentity
	EventID            string
	PublishedAt        time.Time
	Report, Candidate  run.Artifact
	ReportBytes        []byte
}

// MaterializeAnalysisPublication constructs and retains one checked historical
// publication. It fetches no artifacts and performs no registry/approval lookup.
// Exact replay returns owned original bytes without re-verifying prepared evidence;
// changed event/time/report conflicts. Missing historical authority fails closed.
// Store failures are classified/redacted; every error returns nil output. Calls
// are concurrent-safe when the supplied store is. Heavy I/O never occurs in verify.
func MaterializeAnalysisPublication(ctx context.Context, store MaterializationStore, input MaterializationInput) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || input.Provider == "" || input.Principal.ID == "" || input.Principal.Issuer == "" || input.Request.ID == "" || input.Request.Issuer == "" ||
		input.EventID == "" || input.PublishedAt.IsZero() || input.Report.Validate() != nil || input.Report.Kind != "normalized" ||
		input.Report.Format != "analysis-result/v1" || input.Report.MediaType != "application/json" || input.Report.SizeBytes < 1 || input.Report.SizeBytes > 1<<20 {
		return nil, contract.ErrAnalysisPublication
	}
	previous, err := store.GetAnalysisPublication(ctx, input.Principal.ID, input.Request)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil {
		message, err := DecodeAnalysisPublication(previous)
		if err != nil {
			return nil, run.ErrUnavailable
		}
		if !materializationScope(message.Source, message.Data.Context, input) {
			return nil, run.ErrForbidden
		}
		report := input.Report
		claim := contract.AnalysisArtifactClaim{ID: report.ID, RunID: report.RunID, Kind: "analysis", URI: report.URI,
			SHA256: report.SHA256, SizeBytes: json.Number(strconv.FormatInt(report.SizeBytes, 10)), MediaType: report.MediaType, Format: report.Format}
		if message.ID != input.EventID || message.Time != input.PublishedAt.UTC().Format(time.RFC3339Nano) || message.Data.Artifact != claim {
			return nil, run.ErrConflict
		}

		return append([]byte(nil), previous...), nil
	}
	if !errors.Is(err, run.ErrNotFound) {
		return nil, materializationStoreError(ctx, err)
	}
	if len(input.ReportBytes) > 1<<20 {
		return nil, contract.ErrAnalysisPublication
	}
	input.ReportBytes = bytes.Clone(input.ReportBytes)
	record, err := store.GetIntegration(ctx, input.Principal.ID, input.Request)
	if err != nil {
		return nil, materializationStoreError(ctx, err)
	}
	message, err := buildHistoricalPublication(ctx, record, input)
	if err != nil {
		return nil, err
	}
	expected, err := json.Marshal(message) // Builder already validated the closed schema.
	if err != nil {
		return nil, contract.ErrAnalysisPublication
	}
	verify := func(ctx context.Context, retained integration.Record, proposal contract.AnalysisPublication) ([]byte, error) {
		checked, err := buildHistoricalPublication(ctx, retained, input)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(checked, proposal) {
			return nil, contract.ErrAnalysisPublication
		}

		return json.Marshal(checked)
	}
	retained, err := store.RetainAnalysisPublication(ctx, input.Principal.ID, message, verify)
	if err != nil {
		return nil, materializationStoreError(ctx, err)
	}
	if !bytes.Equal(retained, expected) {
		return nil, run.ErrUnavailable
	}

	return bytes.Clone(retained), nil
}

func buildHistoricalPublication(ctx context.Context, record integration.Record, input MaterializationInput) (contract.AnalysisPublication, error) {
	if err := ctx.Err(); err != nil {
		return contract.AnalysisPublication{}, err
	}
	if !materializationScope(record.Disposition.Source, record.Disposition.Data.Context, input) {
		return contract.AnalysisPublication{}, run.ErrForbidden
	}
	if record.Run == nil || record.Verification == nil || record.Verification.Validate(record.Run.Request) != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}

	return BuildAnalysisPublication(ctx, PublicationInput{Admission: record, Provider: input.Provider, Principal: input.Principal,
		EventID: input.EventID, PublishedAt: input.PublishedAt, Report: input.Report, Candidate: input.Candidate, ReportBytes: input.ReportBytes,
		PolicyBytes: record.Verification.PolicyBytes, Producer: record.Verification.Producer, ContractsVersion: record.Verification.ContractsVersion})
}

func materializationScope(provider string, claims contract.IntegrationContext, input MaterializationInput) bool {
	return provider == input.Provider && claims.Principal == input.Principal && claims.Request == input.Request
}

func materializationStoreError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, classified := range []error{context.Canceled, context.DeadlineExceeded, run.ErrNotFound, run.ErrForbidden, run.ErrConflict, run.ErrValidation, contract.ErrAnalysisPublication} {
		if errors.Is(err, classified) {
			return classified
		}
	}

	return run.ErrUnavailable
}
