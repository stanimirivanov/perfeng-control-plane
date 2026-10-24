package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/memory"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type integrationApproverStub struct {
	approverStub
	provider, principalIssuer string
	enabled                   bool
}

func (s integrationApproverStub) IntegrationNamespaces() (string, string, bool) {
	return s.provider, s.principalIssuer, s.enabled
}
func (s integrationApproverStub) ResolveIntegration(ctx context.Context, principal string, _ contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, error) {
	_, err := read(ctx, principal, "baseline", "1.0.0")
	return run.Request{}, err
}

type integrationAuthStub struct{}

func (integrationAuthStub) Authenticate(context.Context, string) (httpapi.Identity, error) {
	return httpapi.Identity{Principal: "alice", CreateRun: true, ReadRun: true}, nil
}

type integrationRuntimeStore struct {
	*memory.Repository
	disposition contract.IntegrationMessage
	reviewed    bool
}

func (s *integrationRuntimeStore) AdmitIntegration(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review integration.Review) (integration.Record, error) {
	_, target, err := review(ctx, principal, message.Data.Context, func(_ context.Context, owner, id, version string) (baseline.Record, error) {
		if owner != principal || id != "baseline" || version != "1.0.0" {
			return baseline.Record{}, run.ErrForbidden
		}
		s.reviewed = true
		return baseline.Record{}, nil
	})
	if err != nil {
		return integration.Record{}, err
	}
	if target != run.ExecutionTargetKubernetes {
		return integration.Record{}, run.ErrValidation
	}
	response := s.disposition
	response.Source = provider
	response.Data.Context = message.Data.Context
	return integration.Record{Disposition: response, Run: &run.Run{ID: response.Data.Run.ID, State: run.StateCreated, Revision: 1, UpdatedAt: time.Now()}}, nil
}
func (s *integrationRuntimeStore) GetIntegration(context.Context, string, contract.CatalogIdentity) (integration.Record, error) {
	return integration.Record{}, run.ErrNotFound
}

func TestIntegrationRuntimeCompositionRequiresDurabilityAndConnectsReview(t *testing.T) {
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-integration/completed.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages []contract.IntegrationMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	message := messages[0]
	message.Data.Context.Principal.ID = "alice"
	source := integrationApproverStub{provider: messages[1].Source, principalIssuer: message.Data.Context.Principal.Issuer, enabled: true}
	if _, err := NewAPIHandler(memory.New(nil), integrationAuthStub{}, source); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled integration accepted memory fallback", err)
	}
	store := &integrationRuntimeStore{Repository: memory.New(nil), disposition: messages[1]}
	handler, err := NewAPIHandler(store, integrationAuthStub{}, source)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/integration/v1/requests", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 202 || !store.reviewed {
		t.Fatal("composition lost reviewed/transaction reader path", w.Code, w.Body.String())
	}
	source.enabled = false
	if _, err := NewAPIHandler(memory.New(nil), integrationAuthStub{}, source); err != nil {
		t.Fatal("legacy composition changed", err)
	}
}
