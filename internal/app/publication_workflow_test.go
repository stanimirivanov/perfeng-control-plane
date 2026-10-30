package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type publicationWorkflowStore struct {
	*evidenceStore
	wire                             []byte
	gets, admissions, writes, checks int
	getErr                           error
	uncertain, invalidate            bool
}

func (store *publicationWorkflowStore) GetAnalysisPublication(_ context.Context, principal string, request contract.CatalogIdentity) ([]byte, error) {
	store.gets++
	if store.getErr != nil {
		return nil, store.getErr
	}
	if store.wire == nil || principal != store.record.Disposition.Data.Context.Principal.ID || request != store.record.Disposition.Data.Context.Request {
		return nil, run.ErrNotFound
	}

	return store.wire, nil
}

func (store *publicationWorkflowStore) GetIntegration(ctx context.Context, principal string, request contract.CatalogIdentity) (integration.Record, error) {
	store.admissions++
	return store.evidenceStore.GetIntegration(ctx, principal, request)
}

func (store *publicationWorkflowStore) RetainAnalysisPublication(ctx context.Context, _ string, proposed contract.AnalysisPublication, verify integration.PublicationVerifier) ([]byte, error) {
	store.writes++
	retained := store.record
	if store.invalidate {
		retained.Verification = nil
	}
	store.checks++
	raw, err := verify(ctx, retained, proposed)
	if err != nil {
		return nil, run.ErrValidation
	}
	store.wire = raw
	if store.uncertain {
		return nil, errors.New("private connection failed after commit")
	}

	return raw, nil
}

// Full semantic report/policy bytes exercise preparation, the real verified
// reader, builder and transactional verifier together. SQL locks remain CI tests.
func publicationWorkflowFixture(t *testing.T) (*publicationWorkflowStore, PublicationEvidenceRequest, reconcile.ArtifactByteReader, *int) {
	t.Helper()
	base, input, _ := evidencePreparationFixture(t)
	document, err := policy.Parse(base.record.Verification.PolicyBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := range document.Spec.Rules {
		document.Spec.Rules[i].Regression = nil
	}
	policyBytes, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(policyBytes)
	pin := hex.EncodeToString(digest[:])
	base.record.Verification.PolicyBytes = policyBytes
	base.record.Run.Request.Policy.SHA256, base.record.Native.Policy.SHA256 = pin, pin
	base.record.Native.Baseline, base.record.Disposition.Data.Context.Baseline = nil, nil
	base.record.Disposition.Data.Context.Policy.SHA256 = pin
	admission := &base.record.Disposition
	admission.SpecVersion, admission.ID, admission.DataContentType = "1.0", "accepted-1", "application/json"
	admission.DataSchema = "https://perfeng.dev/schemas/performance-integration/v1/performance-integration.schema.json"
	admission.Time = admission.Data.Context.RequestedAt
	accepted, err := time.Parse(time.RFC3339Nano, admission.Time)
	if err != nil {
		t.Fatal(err)
	}
	samples, value := int64(2), 140.0
	manifest := analysisresult.Manifest{SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: base.record.Verification.ContractsVersion,
		RunID: base.record.Native.RunID, TestID: base.record.Native.TestID, CreatedAt: accepted.Add(time.Minute).Format(time.RFC3339Nano), Producer: base.record.Verification.Producer,
		Policy:            analysisresult.Policy{ID: base.record.Native.Policy.ID, Version: base.record.Native.Policy.Version, SHA256: pin, Mode: "inform"},
		CandidateArtifact: base.candidate, ReferenceArtifacts: []run.Artifact{}, Evaluations: []analysisresult.Evaluation{{RuleID: "search-latency",
			Metric:  analysisresult.Metric{Name: "ui.search.action_to_visible_ms", Statistic: "mean", Unit: "ms"},
			Quality: analysisresult.Quality{Status: "PASS", Reasons: []string{}, Samples: &samples}, SLO: analysisresult.SLO{Status: "PASS", Reasons: []string{}, Value: &value},
			Regression: analysisresult.Regression{Status: "NOT_EVALUATED", Reasons: []string{"not configured"}}}}}
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest = sha256.Sum256(content)
	base.report.SizeBytes, base.report.SHA256 = int64(len(content)), hex.EncodeToString(digest[:])
	reads := 0
	reader, err := objectstore.NewReader(evidenceGetter(func(_ context.Context, bucket, key string) (objectstore.Object, error) {
		reads++
		if bucket != "perfeng-artifacts" || key != "runs/"+base.record.Native.RunID+"/report.json" {
			t.Fatal("unexpected object read")
		}
		closed := 0

		return objectstore.Object{Body: evidenceBody{Reader: bytes.NewReader(content), closed: &closed}, SizeBytes: int64(len(content)), MediaType: "application/json"}, nil
	}), "perfeng-artifacts", 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	return &publicationWorkflowStore{evidenceStore: base}, input, reader, &reads
}

func TestPublicationWorkflowMaterializesAndReplaysWithoutObjects(t *testing.T) {
	store, input, reader, reads := publicationWorkflowFixture(t)
	raw, err := PublishRegisteredAnalysis(context.Background(), store, reader, input)
	if err != nil || *reads != 1 || store.lookups != 2 || store.writes != 1 || store.checks != 1 {
		t.Fatal("first workflow failed", err)
	}
	message, err := integrationcontract.DecodeAnalysisPublication(raw)
	if err != nil || message.Data.Artifact.SHA256 != store.report.SHA256 || message.Data.Native.Baseline != nil || message.Data.Run.ID == message.Data.Native.RunID {
		t.Fatal("publication lost exact/native/opaque identity evidence", err)
	}
	expected := bytes.Clone(raw)
	raw[0] = '!'
	admissions, gets := store.admissions, store.gets
	store.record.Verification = nil
	input.CandidateID = ""
	replayed, err := PublishRegisteredAnalysis(context.Background(), store, nil, input)
	if err != nil || !bytes.Equal(replayed, expected) || *reads != 1 || store.lookups != 2 || store.writes != 1 || store.admissions != admissions || store.gets != gets+2 {
		t.Fatal("replay loaded preparation or lost original bytes", err)
	}
}

func TestPublicationWorkflowRejectsRetainedConflictsWithoutPreparation(t *testing.T) {
	for _, mode := range []string{"event", "time", "report", "scope", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			store, input, reader, reads := publicationWorkflowFixture(t)
			if _, err := PublishRegisteredAnalysis(context.Background(), store, reader, input); err != nil {
				t.Fatal(err)
			}
			want := run.ErrConflict
			switch mode {
			case "event":
				input.EventID += "-other"
			case "time":
				input.PublishedAt = input.PublishedAt.Add(time.Second)
			case "report":
				input.ReportID = "99999999-9999-4999-8999-999999999999"
			case "scope":
				input.Principal.Issuer += "/other"
				want = run.ErrForbidden
			case "corrupt":
				store.wire = []byte(`{}`)
				want = run.ErrUnavailable
			}
			if raw, err := PublishRegisteredAnalysis(context.Background(), store, nil, input); !errors.Is(err, want) || raw != nil || *reads != 1 || store.writes != 1 || store.lookups != 2 {
				t.Fatal("conflict prepared or replaced evidence", err)
			}
		})
	}
}

func TestPublicationWorkflowUncertainCommitRecoversByReplay(t *testing.T) {
	store, input, reader, reads := publicationWorkflowFixture(t)
	store.uncertain = true
	if raw, err := PublishRegisteredAnalysis(context.Background(), store, reader, input); !errors.Is(err, run.ErrUnavailable) || raw != nil || store.wire == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("uncertain commit returned authority or diagnostics", err)
	}
	input.CandidateID = ""
	store.record.Verification = nil
	if raw, err := PublishRegisteredAnalysis(context.Background(), store, nil, input); err != nil || !bytes.Equal(raw, store.wire) || store.writes != 1 || *reads != 1 {
		t.Fatal("uncertain commit recovery fetched or rewrote evidence", err)
	}
}

func TestPublicationWorkflowFailsWithoutPartialPublication(t *testing.T) {
	for _, mode := range []string{"ledger", "missing", "transaction", "reader", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, input, reader, reads := publicationWorkflowFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := run.ErrUnavailable
			wantReads := 0
			switch mode {
			case "ledger":
				store.getErr = errors.New("private backend details")
			case "missing":
				store.artifactErr = run.ErrNotFound
				want = run.ErrNotFound
			case "transaction":
				store.invalidate = true
				want = run.ErrValidation
				wantReads = 1
			case "reader":
				reader = nil
				want = ErrInvalidConfiguration
			case "cancelled":
				cancel()
				want = context.Canceled
			}
			if raw, err := PublishRegisteredAnalysis(ctx, store, reader, input); !errors.Is(err, want) || raw != nil || store.wire != nil || strings.Contains(err.Error(), "private") || *reads != wantReads {
				t.Fatal("failure leaked partial publication", err)
			}
		})
	}
}
