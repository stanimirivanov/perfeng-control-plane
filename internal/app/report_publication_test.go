package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type reportPublicationStore struct {
	*publicationWorkflowStore
	nativeErr error
	scope     integration.RunAdmissionScope
}

func (store *reportPublicationStore) GetIntegrationForRun(_ context.Context, scope integration.RunAdmissionScope) (integration.Record, error) {
	store.scope = scope
	return store.record, store.nativeErr
}

func reportingPublicationFixture(t *testing.T) (*ReportingPublisher, *reportPublicationStore, run.Run, PublicationEvidenceRequest, *int, *int) {
	t.Helper()
	base, input, reader, reads := publicationWorkflowFixture(t)
	store := &reportPublicationStore{publicationWorkflowStore: base}
	calls := 0
	resolve := func(_ context.Context, request PublicationEvidenceRequest) (PublicationEvent, error) {
		calls++
		expected := input
		expected.EventID, expected.PublishedAt = "", time.Time{}
		if request != expected {
			t.Fatal("identity allocation received substituted request/scope/artifact IDs")
		}

		return PublicationEvent{ID: input.EventID, Time: input.PublishedAt}, nil
	}
	publisher, err := NewReportingPublisher(store, reader, resolve, input.Provider, input.Principal.Issuer)
	if err != nil {
		t.Fatal(err)
	}

	return publisher, store, store.record.Run.Clone(), input, reads, &calls
}

func publishFixtureReport(ctx context.Context, publisher *ReportingPublisher, store *reportPublicationStore, current run.Run, input PublicationEvidenceRequest) error {
	return publisher.PublishReport(ctx, input.Principal.ID, current, reconcile.ReportingInput{Candidate: store.candidate}, store.report)
}

func TestReportingPublisherMaterializesAndRecoversUncertainCommit(t *testing.T) {
	publisher, store, current, input, reads, resolutions := reportingPublicationFixture(t)
	store.uncertain = true
	if err := publishFixtureReport(context.Background(), publisher, store, current, input); !errors.Is(err, run.ErrUnavailable) || store.wire == nil || store.writes != 1 {
		t.Fatal("uncertain publication did not retain recoverable evidence", err)
	}
	if store.scope != (integration.RunAdmissionScope{Provider: input.Provider, Principal: input.Principal, RunID: current.ID}) || *resolutions != 1 || *reads != 1 {
		t.Fatal("publication lost independently configured/authenticated scope")
	}
	message, err := integrationcontract.DecodeAnalysisPublication(store.wire)
	if err != nil || message.ID != input.EventID || message.Time != input.PublishedAt.Format(time.RFC3339Nano) || message.Data.Artifact.ID != input.ReportID {
		t.Fatal("publication lost stable identity or report", err)
	}
	store.record.Verification = nil
	restarted, err := NewReportingPublisher(store, nil, func(context.Context, PublicationEvidenceRequest) (PublicationEvent, error) {
		t.Fatal("retained replay allocated identity")
		return PublicationEvent{}, run.ErrUnavailable
	}, input.Provider, input.Principal.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishFixtureReport(context.Background(), restarted, store, current, input); err != nil || store.writes != 1 || *reads != 1 || *resolutions != 1 {
		t.Fatal("restart replay fetched objects or retried uncertain write", err)
	}
}

func TestReportingPublisherPreservesNonparticipatingHistory(t *testing.T) {
	for _, mode := range []string{"native-only", "legacy", "custom"} {
		t.Run(mode, func(t *testing.T) {
			publisher, store, current, input, reads, resolutions := reportingPublicationFixture(t)
			switch mode {
			case "native-only":
				store.nativeErr = run.ErrNotFound
			case "legacy":
				store.record.Native, store.record.Verification = nil, nil
			case "custom":
				store.record.Verification = nil
			}
			if err := publishFixtureReport(context.Background(), publisher, store, current, input); err != nil || *reads != 0 || *resolutions != 0 || store.writes != 0 || store.lookups != 0 {
				t.Fatal("nonparticipating history blocked reporting or reconstructed authority", err)
			}
		})
	}
}

func TestReportingPublisherRejectsRetainedSubstitutionWithoutAllocation(t *testing.T) {
	for _, mode := range []string{"corrupt", "scope", "report"} {
		t.Run(mode, func(t *testing.T) {
			publisher, store, current, input, reads, resolutions := reportingPublicationFixture(t)
			if err := publishFixtureReport(context.Background(), publisher, store, current, input); err != nil {
				t.Fatal(err)
			}
			want := run.ErrUnavailable
			switch mode {
			case "corrupt":
				store.wire = []byte(`{}`)
			case "scope":
				message, err := integrationcontract.DecodeAnalysisPublication(store.wire)
				if err != nil {
					t.Fatal(err)
				}
				message.Data.Context.Principal.Issuer += "/other"
				store.wire, err = integrationcontract.EncodeAnalysisPublication(message)
				if err != nil {
					t.Fatal(err)
				}
				want = run.ErrForbidden
			case "report":
				store.report.ID = "99999999-9999-4999-8999-999999999999"
				want = run.ErrConflict
			}
			if err := publishFixtureReport(context.Background(), publisher, store, current, input); !errors.Is(err, want) || *reads != 1 || *resolutions != 1 || store.writes != 1 {
				t.Fatal("retained substitution allocated/read/replaced evidence", err)
			}
		})
	}
}

func TestReportingPublisherRejectsSubstitutedAuthority(t *testing.T) {
	for _, mode := range []string{"provider", "principal", "run", "request", "native", "verification", "report", "candidate"} {
		t.Run(mode, func(t *testing.T) {
			publisher, store, current, input, reads, resolutions := reportingPublicationFixture(t)
			switch mode {
			case "provider":
				store.record.Disposition.Source += "/other"
			case "principal":
				store.record.Disposition.Data.Context.Principal.Issuer += "/other"
			case "run":
				store.record.Run.ID += "-other"
			case "request":
				store.record.Run.Request.TestSuite = "another-test"
			case "native":
				store.record.Native.RunID += "-other"
			case "verification":
				store.record.Verification.PolicyBytes = []byte(`{}`)
			case "report":
				store.report.RunID += "-other"
			case "candidate":
				store.candidate.Format = "analysis-result/v1"
			}
			if err := publishFixtureReport(context.Background(), publisher, store, current, input); !errors.Is(err, contract.ErrAnalysisPublication) || *reads != 0 || *resolutions != 0 || store.writes != 0 {
				t.Fatal("substituted authority reached allocation/object/write", err)
			}
		})
	}
}

func TestReportingPublisherClassifiesFailuresAndInvalidEvents(t *testing.T) {
	for _, mode := range []string{"native", "ledger", "resolver", "conflict", "lease", "event", "time", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			publisher, store, current, input, reads, _ := reportingPublicationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := run.ErrUnavailable
			switch mode {
			case "native":
				store.nativeErr = errors.New("private native storage details")
			case "ledger":
				store.getErr = errors.New("private ledger details")
			case "resolver", "conflict", "lease":
				failure := errors.New("private resolver details")
				if mode == "conflict" {
					failure, want = run.ErrConflict, run.ErrConflict
				} else if mode == "lease" {
					failure, want = run.ErrLeaseLost, run.ErrLeaseLost
				}
				publisher.resolve = func(context.Context, PublicationEvidenceRequest) (PublicationEvent, error) {
					return PublicationEvent{}, failure
				}
			case "event", "time":
				want = contract.ErrAnalysisPublication
				publisher.resolve = func(context.Context, PublicationEvidenceRequest) (PublicationEvent, error) {
					if mode == "event" {
						return PublicationEvent{Time: input.PublishedAt}, nil
					}
					return PublicationEvent{ID: input.EventID}, nil
				}
			case "cancelled":
				cancel()
				want = context.Canceled
			}
			err := publishFixtureReport(ctx, publisher, store, current, input)
			if !errors.Is(err, want) || strings.Contains(err.Error(), "private") || *reads != 0 || store.writes != 0 {
				t.Fatal("unsafe/incorrect publication failure", err)
			}
		})
	}
}

func TestReportingPublisherRejectsInvalidConfiguration(t *testing.T) {
	publisher, store, current, input, _, _ := reportingPublicationFixture(t)
	for _, mode := range []string{"store", "resolver", "provider", "namespace"} {
		configuredStore, resolver, provider, namespace := ReportPublicationStore(store), publisher.resolve, input.Provider, input.Principal.Issuer
		switch mode {
		case "store":
			configuredStore = nil
		case "resolver":
			resolver = nil
		case "provider":
			provider = "relative/provider"
		case "namespace":
			namespace = "https://user:secret@example.test"
		}
		if got, err := NewReportingPublisher(configuredStore, nil, resolver, provider, namespace); !errors.Is(err, ErrInvalidConfiguration) || got != nil {
			t.Fatal("invalid configuration accepted", err)
		}
	}
	var missing *ReportingPublisher
	if err := publishFixtureReport(context.Background(), missing, store, current, input); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("nil publisher accepted", err)
	}
}
