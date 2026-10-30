package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/memory"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type integrationApproverStub struct {
	approverStub
	provider, principalIssuer string
	enabled                   bool
	target                    run.ExecutionTarget
}

func (s integrationApproverStub) IntegrationNamespaces() (string, string, bool) {
	return s.provider, s.principalIssuer, s.enabled
}
func (s integrationApproverStub) ResolveIntegrationAdmission(ctx context.Context, principal string, _ contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
	locked, err := read(ctx, principal, "baseline", "1.0.0")
	if err != nil {
		return integration.AdmissionResolution{}, err
	}

	return integration.AdmissionResolution{Target: s.target, Workload: rawresult.Identity{ID: "reviewed-workload", Version: "1.0.0", SHA256: strings.Repeat("a", 64)}, Baseline: &locked}, nil
}

type integrationAuthStub struct{}

func (integrationAuthStub) Authenticate(context.Context, string) (httpapi.Identity, error) {
	return httpapi.Identity{Principal: "alice", CreateRun: true, ReadRun: true}, nil
}

type integrationRuntimeStore struct {
	*memory.Repository
	disposition   contract.IntegrationMessage
	reviewed      bool
	resolved      integration.AdmissionResolution
	reviewError   error
	analysis      []byte
	analysisReads int
}

func (s *integrationRuntimeStore) GetAnalysisPublication(context.Context, string, contract.CatalogIdentity) ([]byte, error) {
	s.analysisReads++
	if s.analysis == nil {
		return nil, run.ErrNotFound
	}

	return s.analysis, nil
}

func (s *integrationRuntimeStore) AdmitIntegration(context.Context, string, string, contract.IntegrationMessage, integration.Review) (integration.Record, error) {
	return integration.Record{}, errors.New("runtime fell back to unbound admission")
}

func (s *integrationRuntimeStore) AdmitIntegrationResolved(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review integration.ResolvedReview) (integration.Record, error) {
	resolved, err := review(ctx, principal, message.Data.Context, func(_ context.Context, owner, id, version string) (baseline.Record, error) {
		if owner != principal || id != "baseline" || version != "1.0.0" {
			return baseline.Record{}, run.ErrForbidden
		}
		s.reviewed = true

		return baseline.Record{}, nil
	})
	s.reviewError = err
	if err != nil {
		return integration.Record{}, err
	}
	s.resolved = resolved
	if resolved.Target != run.ExecutionTargetNativeWindows {
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

func (s *integrationRuntimeStore) CancelIntegration(context.Context, string, contract.IntegrationMessage) (integration.Record, error) {
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
	source := integrationApproverStub{provider: messages[1].Source, principalIssuer: message.Data.Context.Principal.Issuer, enabled: true, target: run.ExecutionTargetNativeWindows}
	if _, err := NewAPIHandler(memory.New(nil), integrationAuthStub{}, source); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled integration accepted memory fallback", err)
	}
	store := &integrationRuntimeStore{Repository: memory.New(nil), disposition: messages[1]}
	type legacyAdmission interface{ integration.Repository }
	legacyStore := struct {
		httpapi.Repository
		legacyAdmission
	}{store, store}
	if _, err := NewAPIHandler(legacyStore, integrationAuthStub{}, source); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled integration accepted legacy-only storage", err)
	}
	type nativeAdmission interface{ integration.NativeRepository }
	withoutRead := struct {
		httpapi.Repository
		legacyAdmission
		nativeAdmission
	}{store, store, store}
	if _, err := NewAPIHandler(withoutRead, integrationAuthStub{}, source); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled integration accepted missing analysis read capability", err)
	}
	legacySource := struct {
		RunApprover
		integrationNamespaces
	}{source, source}
	if _, err := NewAPIHandler(store, integrationAuthStub{}, legacySource); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled integration accepted legacy-only resolver", err)
	}
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
	if w.Code != 202 || !store.reviewed || store.resolved.Workload.ID != "reviewed-workload" || store.resolved.Baseline == nil {
		t.Fatal("composition lost reviewed/transaction reader path", w.Code, w.Body.String())
	}
	if _, err := integrationcontract.DecodeIntegrationMessage(w.Body.Bytes()); err != nil {
		t.Fatal("native capture changed the public disposition", err)
	}
	message.Data.Context.Candidate.Source.Algorithm = "git-sha256"
	message.Data.Context.Candidate.Source.Digest = strings.Repeat("b", 64)
	body, err = json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := httptest.NewRequest("POST", "/integration/v1/requests", strings.NewReader(string(body)))
	unsupported.Header.Set("Authorization", "Bearer test")
	unsupported.Header.Set("Content-Type", "application/json")
	store.reviewed = false
	handler.ServeHTTP(httptest.NewRecorder(), unsupported)
	if !errors.Is(store.reviewError, integration.ErrUnsupportedRevision) || store.reviewed {
		t.Fatal("unsupported source reached registry resolution", store.reviewError)
	}
	source.enabled = false
	if _, err := NewAPIHandler(memory.New(nil), integrationAuthStub{}, source); err != nil {
		t.Fatal("legacy composition changed", err)
	}
}

func TestAnalysisReadRuntimeRoutesWithoutReviewOrOpaqueIDCollision(t *testing.T) {
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-analysis/published.json")
	if err != nil {
		t.Fatal(err)
	}
	message, err := integrationcontract.DecodeAnalysisPublication(raw)
	if err != nil {
		t.Fatal(err)
	}
	message.Data.Context.Principal.ID = "alice"
	raw, err = integrationcontract.EncodeAnalysisPublication(message)
	if err != nil {
		t.Fatal(err)
	}
	store := &integrationRuntimeStore{Repository: memory.New(nil), analysis: raw}
	source := integrationApproverStub{provider: message.Source, principalIssuer: message.Data.Context.Principal.Issuer, enabled: true}
	handler, err := NewAPIHandler(store, integrationAuthStub{}, source)
	if err != nil {
		t.Fatal(err)
	}
	path := "/integration/v1/requests/" + url.PathEscape(message.Data.Context.Request.ID) + "/analysis?issuer=" + url.QueryEscape(message.Data.Context.Request.Issuer)
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != string(raw) || store.analysisReads != 1 || store.reviewed {
		t.Fatal("read routing triggered review or changed bytes", w.Code)
	}
	// Encoded /analysis belongs to the opaque lifecycle ID, not the route suffix.
	r = httptest.NewRequest("GET", "/integration/v1/requests/request%2Fanalysis?issuer="+url.QueryEscape(message.Data.Context.Request.Issuer), nil)
	r.Header.Set("Authorization", "Bearer test")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 404 || store.analysisReads != 1 || store.reviewed {
		t.Fatal("opaque lifecycle ID misrouted to analysis")
	}
}
