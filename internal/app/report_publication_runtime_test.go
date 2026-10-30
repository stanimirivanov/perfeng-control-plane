package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type durablePublicationStore struct {
	*reportPublicationStore
	reservation integration.PublicationEventReservation
	reserved    integration.PublicationEventRequest
	reserveErr  error
	allocations int
}

func (store *durablePublicationStore) ReserveAnalysisPublicationEvent(_ context.Context, input integration.PublicationEventRequest) (integration.PublicationEventReservation, error) {
	store.allocations++
	store.reserved = input
	return store.reservation, store.reserveErr
}

type publicationRuntimeStore struct {
	inertReconciliationStore
	*durablePublicationStore
	t                 *testing.T
	claim             run.Claim
	expectPublication bool
	advances          int
	advanceErr        error
}

func (store *publicationRuntimeStore) GetArtifact(ctx context.Context, principal, runID, id string) (run.Artifact, error) {
	return store.reportPublicationStore.GetArtifact(ctx, principal, runID, id)
}

func (store *publicationRuntimeStore) ListArtifacts(context.Context, string, string) ([]run.Artifact, error) {
	return []run.Artifact{store.candidate, store.report}, nil
}

func (store *publicationRuntimeStore) AdvanceClaim(_ context.Context, lease run.Lease, revision int64, change run.Change) (run.Run, error) {
	store.advances++
	if lease != store.claim.Lease || revision != store.claim.Run.Revision || change.State != run.StateCompleted || (store.wire != nil) != store.expectPublication {
		store.t.Fatal("completion lost publication ordering or original lease/revision")
	}

	return store.claim.Run, store.advanceErr
}

func durablePublisherFixture(t *testing.T) (*ReportingPublisher, *publicationRuntimeStore, PublicationEvidenceRequest, *int) {
	t.Helper()
	base, _, current, input, reads, _ := reportingPublicationFixture(t)
	store := &publicationRuntimeStore{durablePublicationStore: &durablePublicationStore{reportPublicationStore: base.store.(*reportPublicationStore),
		reservation: integration.PublicationEventReservation{EventID: input.EventID, PublishedAt: input.PublishedAt}}, t: t, expectPublication: true}
	current.State, current.Revision = run.StateReporting, 1
	store.claim = run.Claim{Run: current, Lease: run.Lease{RunID: current.ID, Principal: input.Principal.ID, WorkerID: "worker-a", Token: strings.Repeat("a", 32)}}
	publisher, err := NewDurableReportingPublisher(store, base.reader, input.Provider, input.Principal.Issuer)
	if err != nil {
		t.Fatal(err)
	}

	return publisher, store, input, reads
}

func TestDurableReportingStagePublishesBeforeCompletionAndReplays(t *testing.T) {
	publisher, store, input, reads := durablePublisherFixture(t)
	namespaces := integrationApproverStub{provider: input.Provider, principalIssuer: input.Principal.Issuer, enabled: true}
	composed, err := composeReportingPublisher(true, store, publisher.reader, namespaces)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, kubernetesClient := reconciliationDependencies(t)
	dependencies.Store, dependencies.Publisher = store, composed
	stage, err := newReportingStage(dependencies, reconcile.DefaultReportingConfig())
	if err != nil {
		t.Fatal(err)
	}
	if store.allocations != 0 || store.gets != 0 || *reads != 0 {
		t.Fatal("construction performed publication I/O")
	}
	store.advanceErr = run.ErrUnavailable
	if _, err := stage.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) || store.wire == nil {
		t.Fatal("publication/advance failure lost retained evidence", err)
	}
	expected := integration.PublicationEventRequest{Provider: input.Provider, Principal: input.Principal, Request: input.Request, ReportID: input.ReportID, CandidateID: input.CandidateID}
	if store.reserved != expected || store.allocations != 1 || *reads != 1 {
		t.Fatal("durable allocation lost exact scope/evidence")
	}
	store.advanceErr = nil
	if _, err := stage.Reconcile(context.Background(), store.claim); err != nil || store.advances != 2 || store.allocations != 1 || store.writes != 1 || *reads != 1 {
		t.Fatal("advance recovery regenerated identity or evidence", err)
	}
	if len(kubernetesClient.Actions()) != 0 {
		t.Fatal("registered report recovery contacted Kubernetes")
	}
}

func TestDurableReportingResolverHandlesUncertainAllocationAndErrors(t *testing.T) {
	publisher, store, input, reads := durablePublisherFixture(t)
	store.reserveErr = errors.New("private commit failure")
	request := PublicationEvidenceRequest{Provider: input.Provider, Principal: input.Principal, Request: input.Request, ReportID: input.ReportID, CandidateID: input.CandidateID}
	if event, err := publisher.resolve(context.Background(), request); !errors.Is(err, run.ErrUnavailable) || event != (PublicationEvent{}) || strings.Contains(err.Error(), "private") {
		t.Fatal("uncertain allocation exposed identity/details", err)
	}
	store.reserveErr = nil
	if err := publishFixtureReport(context.Background(), publisher, store.reportPublicationStore, store.claim.Run, input); err != nil || store.allocations != 2 || store.writes != 1 || *reads != 1 {
		t.Fatal("allocation retry failed to materialize retained event", err)
	}
	for _, failure := range []error{run.ErrConflict, run.ErrLeaseLost, context.Canceled} {
		store.reserveErr = failure
		if event, err := publisher.resolve(context.Background(), request); !errors.Is(err, failure) || event != (PublicationEvent{}) {
			t.Fatal("classified allocation error changed", err)
		}
	}
	request.EventID = "caller-selected"
	if event, err := publisher.resolve(context.Background(), request); !errors.Is(err, ErrInvalidConfiguration) || event != (PublicationEvent{}) {
		t.Fatal("resolver accepted preassigned event", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allocations := store.allocations
	if event, err := publisher.resolve(ctx, request); !errors.Is(err, context.Canceled) || event != (PublicationEvent{}) || store.allocations != allocations {
		t.Fatal("cancelled resolver accessed allocation storage", err)
	}
}

func TestReportingPublicationCompositionDefaultsOffAndFailsClosed(t *testing.T) {
	publisher, store, input, reads := durablePublisherFixture(t)
	if got, err := composeReportingPublisher(false, nil, nil, nil); err != nil || got != nil {
		t.Fatal("disabled composition required new capabilities", err)
	}
	store.expectPublication = false
	dependencies, _ := reconciliationDependencies(t)
	dependencies.Store = store
	stage, err := newReportingStage(dependencies, reconcile.DefaultReportingConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Reconcile(context.Background(), store.claim); err != nil || store.advances != 1 || store.allocations != 0 || store.gets != 0 || *reads != 0 {
		t.Fatal("disabled reporting attempted publication", err)
	}
	for _, mode := range []string{"namespace", "disabled registry", "capability", "reader"} {
		namespaces := integrationNamespaces(integrationApproverStub{provider: input.Provider, principalIssuer: input.Principal.Issuer, enabled: true})
		configuredStore, reader := ReconciliationStore(store), publisher.reader
		switch mode {
		case "namespace":
			namespaces = nil
		case "disabled registry":
			namespaces = integrationApproverStub{}
		case "capability":
			configuredStore = inertReconciliationStore{}
		case "reader":
			reader = nil
		}
		if got, err := composeReportingPublisher(true, configuredStore, reader, namespaces); !errors.Is(err, ErrInvalidConfiguration) || got != nil {
			t.Fatal("enabled composition accepted missing authority/capability", mode, err)
		}
	}
}

func TestApplicationPublicationSwitchRequiresReviewedIntegration(t *testing.T) {
	values := validApplicationEnvironment(t)
	config, err := loadApplicationConfig(mapLookup(values))
	if err != nil || config.AnalysisPublicationEnabled {
		t.Fatal("publication was enabled by default", err)
	}
	values[analysisPublicationVariable] = "true"
	config, err = loadApplicationConfig(mapLookup(values))
	if err != nil || !config.AnalysisPublicationEnabled {
		t.Fatal("explicit publication switch ignored", err)
	}
	resources := validRuntimeResourcesForTest(t, config)
	if got, err := NewApplicationRuntime(config, resources, testLogger()); !errors.Is(err, ErrInvalidConfiguration) || got != nil {
		t.Fatal("publication enabled without reviewed integration namespaces", err)
	}
	values[analysisPublicationVariable] = "false"
	if disabled, err := loadApplicationConfig(mapLookup(values)); err != nil || disabled.AnalysisPublicationEnabled {
		t.Fatal("explicit false publication switch ignored", err)
	}
	values[analysisPublicationVariable] = "yes"
	if _, err := loadApplicationConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("malformed publication switch accepted", err)
	}
}
