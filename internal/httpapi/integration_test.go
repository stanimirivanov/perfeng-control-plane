package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type integrationStoreStub struct {
	result integration.Record
	err    error
	calls  int
}

func (s *integrationStoreStub) CancelIntegration(ctx context.Context, principal string, message contract.IntegrationMessage) (integration.Record, error) {
	return s.GetIntegration(ctx, principal, message.Data.Context.Request)
}

func (s *integrationStoreStub) AdmitIntegration(_ context.Context, principal, provider string, message contract.IntegrationMessage, _ integration.Review) (integration.Record, error) {
	s.calls++
	if principal != "alice" || provider != s.result.Disposition.Source || message.Data.Context.Principal.ID != principal {
		return integration.Record{}, run.ErrForbidden
	}
	return s.result, s.err
}
func (s *integrationStoreStub) GetIntegration(_ context.Context, principal string, id contract.CatalogIdentity) (integration.Record, error) {
	s.calls++
	if principal != "alice" || id != s.result.Disposition.Data.Context.Request {
		return integration.Record{}, run.ErrNotFound
	}
	return s.result, s.err
}

func integrationHTTPFixture(t *testing.T) (contract.IntegrationMessage, *integrationStoreStub, IntegrationOptions) {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-integration/completed.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages []contract.IntegrationMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	request, accepted := messages[0], messages[1]
	request.Data.Context.Principal.ID = "alice"
	request.Data.Context.Request.ID = "request/with:separators?#"
	accepted.Data.Context = request.Data.Context
	when, err := time.Parse(time.RFC3339Nano, accepted.Time)
	if err != nil {
		t.Fatal(err)
	}
	store := &integrationStoreStub{result: integration.Record{Disposition: accepted, Run: &run.Run{ID: accepted.Data.Run.ID, State: run.StateCreated, Revision: 1, CreatedAt: when, UpdatedAt: when}}}
	options := IntegrationOptions{Provider: accepted.Source, PrincipalIssuer: request.Data.Context.Principal.Issuer, Decode: integrationcontract.DecodeIntegrationMessage, Encode: integrationcontract.EncodeIntegrationMessage, Review: func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return run.Request{}, "", nil
	}}
	return request, store, options
}

func TestIntegrationHTTPAdmissionGuardsAndProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*http.Request, *Identity, *integrationStoreStub)
		status int
		code   string
		calls  int
	}{
		{"accepted", nil, 202, "", 1},
		{"missing bearer", func(r *http.Request, i *Identity, s *integrationStoreStub) { r.Header.Del("Authorization") }, 401, "unauthenticated", 0},
		{"duplicate bearer", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			r.Header.Add("Authorization", "Bearer second")
		}, 401, "unauthenticated", 0},
		{"permission", func(r *http.Request, i *Identity, s *integrationStoreStub) { i.CreateRun = false }, 403, "forbidden", 0},
		{"principal", func(r *http.Request, i *Identity, s *integrationStoreStub) { i.Principal = "bob" }, 403, "forbidden", 0},
		{"namespace", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			replaceIntegrationBody(r, `"issuer":"https://auth.example`, `"issuer":"https://forged.example`)
		}, 403, "forbidden", 0},
		{"media", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			r.Header.Set("Content-Type", "text/plain")
		}, 415, "invalid_request", 0},
		{"encoding", func(r *http.Request, i *Identity, s *integrationStoreStub) { r.Header.Set("Content-Encoding", "gzip") }, 415, "invalid_request", 0},
		{"query", func(r *http.Request, i *Identity, s *integrationStoreStub) { r.URL.RawQuery = "issuer=forged" }, 400, "invalid_request", 0},
		{"malformed", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			r.Body = io.NopCloser(strings.NewReader(`{`))
		}, 400, "invalid_request", 0},
		{"duplicate key", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			replaceIntegrationBody(r, `"specversion":`, `"id":"duplicate","specversion":`)
		}, 400, "invalid_request", 0},
		{"version", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			replaceIntegrationBody(r, `"specversion":"1.0"`, `"specversion":"2.0"`)
		}, 400, "unsupported_version", 0},
		{"oversized", func(r *http.Request, i *Identity, s *integrationStoreStub) {
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", MaxBodyBytes+1)))
		}, 400, "invalid_request", 0},
		{"conflict", func(r *http.Request, i *Identity, s *integrationStoreStub) { s.err = run.ErrConflict }, 409, "conflict", 1},
		{"private backend", func(r *http.Request, i *Identity, s *integrationStoreStub) { s.err = errors.New("private-backend") }, 503, "unavailable", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, store, options := integrationHTTPFixture(t)
			raw, err := options.Encode(request)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", integrationPath, strings.NewReader(string(raw)))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("Content-Type", "application/json")
			identity := Identity{Principal: "alice", CreateRun: true, ReadRun: true}
			if tc.change != nil {
				tc.change(r, &identity, store)
			}
			h, err := NewIntegrationHandler(store, func(context.Context, string) (Identity, error) { return identity, nil }, options)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || store.calls != tc.calls || strings.Contains(w.Body.String(), "private-backend") {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, store.calls, w.Body.String())
			}
			if tc.code != "" {
				var result map[string]any
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result) != 2 || result["code"] != tc.code {
					t.Fatal("invalid protocol error", w.Body.String())
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" || tc.status == 503 && w.Header().Get("Retry-After") == "" {
				t.Fatal("missing recovery/cache headers")
			}
		})
	}
}

func replaceIntegrationBody(r *http.Request, old, new string) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(strings.Replace(string(raw), old, new, 1)))
}

func TestIntegrationHTTPScopedSnapshotsAndStateProjection(t *testing.T) {
	request, store, options := integrationHTTPFixture(t)
	identity := Identity{Principal: "alice", ReadRun: true}
	h, err := NewIntegrationHandler(store, func(context.Context, string) (Identity, error) { return identity, nil }, options)
	if err != nil {
		t.Fatal(err)
	}
	path := integrationPath + "/" + url.PathEscape(request.Data.Context.Request.ID) + "?issuer=" + url.QueryEscape(request.Data.Context.Request.Issuer)
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for state, expected := range map[run.State]string{run.StateCreated: "ACCEPTED", run.StateValidating: "ACCEPTED", run.StateProvisioning: "ACCEPTED", run.StateWarmingUp: "ACCEPTED", run.StateRunning: "RUNNING", run.StateCollecting: "RUNNING", run.StateAnalyzing: "RUNNING", run.StateReporting: "RUNNING", run.StateCancelling: "CANCELLING", run.StateCompleted: "COMPLETED", run.StateInvalid: "FAILED", run.StateInfrastructureFailure: "FAILED", run.StateTestFailure: "FAILED", run.StateAborted: "CANCELLED"} {
		store.result.Run.State = state
		first, second := get(path), get(path)
		message, err := options.Decode(first.Body.Bytes())
		if first.Code != 200 || err != nil || message.Data.State != expected || message.Data.Revision == nil || first.Body.String() != second.Body.String() {
			t.Fatal("unstable/wrong snapshot", state, first.Body.String(), err)
		}
	}
	identity.ReadRun = false
	if got := get(path); got.Code != 403 {
		t.Fatal("read permission bypass", got.Code)
	}
	identity.ReadRun = true
	identity.Principal = "bob"
	if got := get(path); got.Code != 404 {
		t.Fatal("cross-principal disclosure", got.Code)
	}
	identity.Principal = "alice"
	for _, bad := range []string{integrationPath + "/id", path + "&issuer=other", path + "&extra=value"} {
		if got := get(bad); got.Code != 400 {
			t.Fatal("query not closed", got.Code)
		}
	}
	store.result.Run = nil
	store.result.Disposition.Type = "dev.perfeng.integration.rejected.v1"
	store.result.Disposition.Data.Run = nil
	store.result.Disposition.Data.Error = &contract.IntegrationError{Code: "unavailable_reference", Message: "Unavailable"}
	if got := get(path); got.Code != 422 {
		t.Fatal("lost retained rejection", got.Code, got.Body.String())
	}
}
