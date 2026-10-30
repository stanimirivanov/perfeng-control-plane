package integrationcontract

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// The real ledger's atomicity/locks are exercised by PostgreSQL tests. This
// sequential fake exercises the trusted verifier and materializer boundaries.
type materializationStore struct {
	afterCommitErr                 error
	record                         integration.Record
	wire                           []byte
	getErr, readErr, saveErr       error
	reads, attempts, verifications int
	change                         func(*integration.Record, *contract.AnalysisPublication)
	corrupt                        bool
}

func (store *materializationStore) GetIntegration(context.Context, string, contract.CatalogIdentity) (integration.Record, error) {
	store.reads++
	return store.record, store.readErr
}

func (store *materializationStore) GetAnalysisPublication(context.Context, string, contract.CatalogIdentity) ([]byte, error) {
	if store.getErr != nil {
		return nil, store.getErr
	}
	if store.wire == nil {
		return nil, run.ErrNotFound
	}

	return store.wire, nil
}

func (store *materializationStore) RetainAnalysisPublication(ctx context.Context, _ string, message contract.AnalysisPublication, verify integration.PublicationVerifier) ([]byte, error) {
	store.attempts++
	if store.saveErr != nil {
		return nil, store.saveErr
	}
	retained := store.record
	if store.change != nil {
		store.change(&retained, &message)
	}
	store.verifications++
	raw, err := verify(ctx, retained, message)
	if err != nil {
		return nil, err
	}
	if store.corrupt {
		return []byte(`{}`), nil
	}
	store.wire = raw
	if store.afterCommitErr != nil {
		return nil, store.afterCommitErr
	}

	return raw, nil
}

func materializationFixture(t *testing.T) (*materializationStore, MaterializationInput) {
	t.Helper()
	input := publicationInput(t)
	evidence := integration.AnalysisVerification{PolicyBytes: input.PolicyBytes, Producer: input.Producer, ContractsVersion: input.ContractsVersion}.Clone()
	input.Admission.Verification = &evidence

	return &materializationStore{record: input.Admission}, MaterializationInput{Provider: input.Provider, Principal: input.Principal,
		Request: input.Admission.Disposition.Data.Context.Request, EventID: input.EventID, PublishedAt: input.PublishedAt,
		Report: input.Report, Candidate: input.Candidate, ReportBytes: input.ReportBytes}
}

func TestMaterializationRetainsHistoricalEvidenceAndReplaysOwnedBytes(t *testing.T) {
	store, input := materializationFixture(t)
	store.record.Run.State = run.StateAborted // Historical late analysis remains valid.
	raw, err := MaterializeAnalysisPublication(context.Background(), store, input)
	if err != nil || store.reads != 1 || store.attempts != 1 || store.verifications != 1 {
		t.Fatal("first materialization", err)
	}
	message, err := DecodeAnalysisPublication(raw)
	if err != nil || message.Data.Artifact.SHA256 != publicationDigest(input.ReportBytes) || message.Data.Native.Baseline == nil {
		t.Fatal("report bytes/outcomes or historical association changed", err)
	}
	expected := bytes.Clone(raw)
	raw[0] = '!'
	input.ReportBytes, input.Candidate = nil, run.Artifact{}
	store.record.Verification = nil // Replay needs no current reconstruction or bytes.
	replayed, err := MaterializeAnalysisPublication(context.Background(), store, input)
	if err != nil || !bytes.Equal(replayed, expected) || store.reads != 1 || store.attempts != 1 {
		t.Fatal("replay reverified or lost owned original", err)
	}
	replayed[0] = '!'
	if bytes.Equal(replayed, store.wire) {
		t.Fatal("replay returned store-owned bytes")
	}
}

func TestMaterializationRejectsConflictingReplay(t *testing.T) {
	for _, change := range []func(*MaterializationInput){
		func(i *MaterializationInput) { i.EventID += "-other" },
		func(i *MaterializationInput) { i.PublishedAt = i.PublishedAt.Add(time.Second) },
		func(i *MaterializationInput) { i.Report.ID = "99999999-9999-4999-8999-999999999999" },
		func(i *MaterializationInput) { i.Report.SHA256 = strings.Repeat("f", 64) },
	} {
		store, input := materializationFixture(t)
		if _, err := MaterializeAnalysisPublication(context.Background(), store, input); err != nil {
			t.Fatal(err)
		}
		change(&input)
		if raw, err := MaterializeAnalysisPublication(context.Background(), store, input); !errors.Is(err, run.ErrConflict) || raw != nil || store.attempts != 1 {
			t.Fatal("conflict replaced retained publication", err)
		}
	}
}

func TestMaterializationRequiresHistoricalScopeAndAuthority(t *testing.T) {
	for _, mode := range []string{"missing", "version", "policy", "principal", "provider", "request", "transaction", "proposal", "report"} {
		t.Run(mode, func(t *testing.T) {
			store, input := materializationFixture(t)
			want := contract.ErrAnalysisPublication
			switch mode {
			case "missing":
				store.record.Verification = nil
			case "version":
				store.record.Verification.ContractsVersion = "0.25.0"
			case "policy":
				store.record.Verification.PolicyBytes = []byte(`{}`)
			case "principal":
				input.Principal.Issuer += "/other"
				want = run.ErrForbidden
			case "provider":
				input.Provider += "/other"
				want = run.ErrForbidden
			case "request":
				input.Request.ID += "-other"
				want = run.ErrForbidden
			case "transaction":
				store.change = func(record *integration.Record, _ *contract.AnalysisPublication) { record.Verification = nil }
			case "proposal":
				store.change = func(_ *integration.Record, proposal *contract.AnalysisPublication) { proposal.ID += "-forged" }
			case "report":
				input.ReportBytes[0] = '!'
			}
			if raw, err := MaterializeAnalysisPublication(context.Background(), store, input); !errors.Is(err, want) || raw != nil || store.wire != nil {
				t.Fatal("unchecked historical publication retained", err)
			}
		})
	}
}

func TestMaterializationRecoversUncertainCommitByExactReplay(t *testing.T) {
	store, input := materializationFixture(t)
	store.afterCommitErr = errors.New("private connection failure after commit")
	if raw, err := MaterializeAnalysisPublication(context.Background(), store, input); !errors.Is(err, run.ErrUnavailable) || raw != nil || store.wire == nil {
		t.Fatal("uncertain commit leaked output or lost recovery fixture", err)
	}
	input.ReportBytes, input.Candidate = nil, run.Artifact{}
	raw, err := MaterializeAnalysisPublication(context.Background(), store, input)
	if err != nil || !bytes.Equal(raw, store.wire) || store.attempts != 1 {
		t.Fatal("recovery repeated uncertain write", err)
	}
}

func TestMaterializationClassifiesFailuresWithoutPartialOutput(t *testing.T) {
	for _, mode := range []string{"read", "load", "conflict", "corrupt-write", "corrupt-read", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, input := materializationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := run.ErrUnavailable
			switch mode {
			case "read":
				store.getErr = errors.New("private backend details")
			case "load":
				store.readErr = errors.New("private backend details")
			case "conflict":
				store.saveErr, want = run.ErrConflict, run.ErrConflict
			case "corrupt-write":
				store.corrupt = true
			case "corrupt-read":
				store.wire = []byte(`{}`)
			case "cancelled":
				cancel()
				want = context.Canceled
			}
			if raw, err := MaterializeAnalysisPublication(ctx, store, input); !errors.Is(err, want) || raw != nil || strings.Contains(err.Error(), "private") {
				t.Fatal("failure leaked payload or diagnostics", err)
			}
		})
	}
}
