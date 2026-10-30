package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const evidenceCandidateID = "44444444-4444-4444-8444-444444444444"

type evidenceStore struct {
	record               integration.Record
	report, candidate    run.Artifact
	readErr, artifactErr error
	lookups              int
}

func (store *evidenceStore) GetIntegration(context.Context, string, contract.CatalogIdentity) (integration.Record, error) {
	return store.record, store.readErr
}

func (store *evidenceStore) GetArtifact(_ context.Context, principal, runID, id string) (run.Artifact, error) {
	store.lookups++
	if principal != store.record.Disposition.Data.Context.Principal.ID || runID != store.record.Run.ID {
		return run.Artifact{}, run.ErrForbidden
	}
	if store.artifactErr != nil {
		return run.Artifact{}, store.artifactErr
	}
	if id == evidenceCandidateID {
		return store.candidate, nil
	}

	return store.report, nil
}

type evidenceGetter func(context.Context, string, string) (objectstore.Object, error)

func (get evidenceGetter) GetObject(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	return get(ctx, bucket, key)
}

type evidenceBody struct {
	io.Reader
	closed   *int
	closeErr error
}

func (body evidenceBody) Close() error {
	*body.closed++
	return body.closeErr
}

func evidencePreparationFixture(t *testing.T) (*evidenceStore, PublicationEvidenceRequest, []byte) {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-analysis/published.json")
	if err != nil {
		t.Fatal(err)
	}
	message, err := integrationcontract.DecodeAnalysisPublication(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = contract.Files.ReadFile("snapshot/examples/create.json")
	var nativeRequest run.Request
	if err != nil || json.Unmarshal(raw, &nativeRequest) != nil {
		t.Fatal("native request", err)
	}
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := policy.Parse(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(policyBytes)
	nativeRequest.TestSuite = message.Data.Native.TestID
	nativeRequest.Policy = run.Reference{ID: document.Metadata.Name, Version: document.Metadata.Version, SHA256: hex.EncodeToString(digest[:])}
	message.Data.Native.Policy = contract.AnalysisNativeIdentity{ID: nativeRequest.Policy.ID, Version: nativeRequest.Policy.Version, SHA256: nativeRequest.Policy.SHA256}
	admission := contract.IntegrationMessage{Source: message.Source, Type: "dev.perfeng.integration.accepted.v1", Data: contract.IntegrationData{Context: message.Data.Context, Run: &message.Data.Run}}
	admission.Data.Run = &contract.CatalogIdentity{Issuer: message.Source, ID: "opaque-provider-run"}
	verification := &integration.AnalysisVerification{PolicyBytes: policyBytes, ContractsVersion: "0.25.0",
		Producer: rawresult.Producer{Name: "perfeng-analysis", Version: "1.0.0", Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("f", 64)}}
	// Preparation verifies reference bytes, not report semantics; the materializer
	// separately proves schema/provenance/outcomes using the full builder fixtures.
	content := []byte("{\"report\":\"exact\"}\n")
	digest = sha256.Sum256(content)
	report := run.Artifact{ID: message.Data.Artifact.ID, RunID: message.Data.Native.RunID, Kind: "normalized", Format: "analysis-result/v1",
		URI: "s3://perfeng-artifacts/runs/" + message.Data.Native.RunID + "/report.json", MediaType: "application/json", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	candidate := report
	candidate.ID, candidate.Format, candidate.URI = evidenceCandidateID, "normalized-result/v1", strings.Replace(report.URI, "report.json", "candidate.json", 1)

	return &evidenceStore{report: report, candidate: candidate, record: integration.Record{Disposition: admission, Native: &message.Data.Native, Verification: verification,
			Run: &run.Run{ID: report.RunID, Request: nativeRequest, State: run.StateAborted}}},
		PublicationEvidenceRequest{Provider: message.Source, Principal: message.Data.Context.Principal, Request: message.Data.Context.Request,
			EventID: "publication-1", PublishedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), ReportID: report.ID, CandidateID: candidate.ID}, content
}

func TestPublicationEvidencePreparationUsesScopedRegisteredReport(t *testing.T) {
	store, input, content := evidencePreparationFixture(t)
	reads, closed := 0, 0
	reader, err := objectstore.NewReader(evidenceGetter(func(_ context.Context, bucket, key string) (objectstore.Object, error) {
		reads++
		if bucket != "perfeng-artifacts" || key != "runs/"+store.record.Run.ID+"/report.json" {
			t.Fatal("unscoped or candidate object read")
		}

		return objectstore.Object{Body: evidenceBody{Reader: bytes.NewReader(content), closed: &closed}, SizeBytes: int64(len(content)), MediaType: "application/json"}, nil
	}), "perfeng-artifacts", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PrepareAnalysisPublication(context.Background(), store, reader, input)
	if err != nil || reads != 1 || closed != 1 || store.lookups != 2 || !bytes.Equal(got.ReportBytes, content) || got.Report != store.report || got.Candidate != store.candidate || got.Request != input.Request {
		t.Fatal("scoped preparation failed", err)
	}
	got.ReportBytes[0] = '!'
	if bytes.Equal(got.ReportBytes, content) {
		t.Fatal("prepared bytes alias source")
	}
}

func TestPublicationEvidenceRejectsSubstitutionBeforeObjectRead(t *testing.T) {
	for _, mode := range []string{"principal", "native-run", "missing", "report-id", "report-run", "oversize", "candidate-format", "read-absence", "artifact-absence", "artifact-failure"} {
		t.Run(mode, func(t *testing.T) {
			store, input, _ := evidencePreparationFixture(t)
			want := contract.ErrAnalysisPublication
			switch mode {
			case "principal":
				input.Principal.Issuer += "/other"
				want = run.ErrForbidden
			case "native-run":
				store.record.Native.RunID += "-other"
			case "missing":
				store.record.Verification = nil
			case "report-id":
				store.report.ID = "99999999-9999-4999-8999-999999999999"
			case "report-run":
				store.report.RunID += "-other"
			case "oversize":
				store.report.SizeBytes = (1 << 20) + 1
			case "candidate-format":
				store.candidate.Format = "raw-result/v1"
			case "read-absence":
				store.readErr = run.ErrNotFound
				want = run.ErrNotFound
			case "artifact-failure":
				store.artifactErr = errors.New("private storage details")
				want = run.ErrUnavailable
			case "artifact-absence":
				store.artifactErr = run.ErrNotFound
				want = run.ErrNotFound
			}
			reader, err := objectstore.NewReader(evidenceGetter(func(context.Context, string, string) (objectstore.Object, error) {
				t.Fatal("rejected scope/reference triggered object I/O")
				return objectstore.Object{}, run.ErrUnavailable
			}), "perfeng-artifacts", 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareAnalysisPublication(context.Background(), store, reader, input)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, integrationcontract.MaterializationInput{}) || strings.Contains(err.Error(), "private") {
				t.Fatal("rejection leaked partial evidence", err)
			}
		})
	}
}

func TestPublicationEvidenceReaderFailuresAreClosedAndRedacted(t *testing.T) {
	for _, mode := range []string{"bucket", "path", "hash", "size", "media", "absent", "backend", "close", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, input, content := evidencePreparationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := contract.ErrAnalysisPublication
			reads, closed := 0, 0
			switch mode {
			case "bucket":
				store.report.URI = strings.Replace(store.report.URI, "perfeng-artifacts", "other-bucket", 1)
			case "path":
				store.report.URI = "s3://perfeng-artifacts/runs/other/report.json"
			case "hash":
				content[0] = '!'
			case "absent":
				want = run.ErrNotFound
			case "backend", "close":
				want = run.ErrUnavailable
			case "cancelled":
				cancel()
				want = context.Canceled
			}
			reader, err := objectstore.NewReader(evidenceGetter(func(context.Context, string, string) (objectstore.Object, error) {
				reads++
				if mode == "absent" {
					return objectstore.Object{}, objectstore.ErrObjectNotFound
				}
				if mode == "backend" {
					return objectstore.Object{}, errors.New("private signed request")
				}
				body := evidenceBody{Reader: bytes.NewReader(content), closed: &closed}
				if mode == "close" {
					body.closeErr = errors.New("private close failure")
				}
				object := objectstore.Object{Body: body, SizeBytes: int64(len(content)), MediaType: "application/json"}
				if mode == "size" {
					object.SizeBytes++
				}
				if mode == "media" {
					object.MediaType = "text/plain"
				}

				return object, nil
			}), "perfeng-artifacts", 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			got, err := PrepareAnalysisPublication(ctx, store, reader, input)
			if !errors.Is(err, want) || !reflect.DeepEqual(got, integrationcontract.MaterializationInput{}) || strings.Contains(err.Error(), "private") {
				t.Fatal("reader failure leaked evidence or diagnostics", err)
			}
			if (mode == "bucket" || mode == "path" || mode == "cancelled") && reads != 0 {
				t.Fatal("invalid location/cancellation contacted storage")
			}
			if reads != 0 && mode != "absent" && mode != "backend" && closed != 1 {
				t.Fatal("response body not closed")
			}
		})
	}
}
