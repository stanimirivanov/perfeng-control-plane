package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type analysisReadStub struct {
	raw       []byte
	err       error
	principal string
	request   contract.CatalogIdentity
	calls     int
}

func (store *analysisReadStub) GetAnalysisPublication(_ context.Context, principal string, request contract.CatalogIdentity) ([]byte, error) {
	store.calls++
	if principal != store.principal || request != store.request {
		return nil, run.ErrNotFound
	}

	return store.raw, store.err
}

func analysisReadFixture(t *testing.T) (*analysisReadStub, AnalysisReadOptions, string) {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-analysis/published.json")
	if err != nil {
		t.Fatal(err)
	}
	message, err := integrationcontract.DecodeAnalysisPublication(raw)
	if err != nil {
		t.Fatal(err)
	}
	message.Data.Context.Request.ID = "request/with:separators?#"
	raw, err = integrationcontract.EncodeAnalysisPublication(message)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve noncanonical whitespace too; transport must not reserialize bytes.
	raw = append(append([]byte(" \n"), raw...), '\n')
	store := &analysisReadStub{raw: raw, principal: message.Data.Context.Principal.ID, request: message.Data.Context.Request}
	options := AnalysisReadOptions{Provider: message.Source, PrincipalIssuer: message.Data.Context.Principal.Issuer, Decode: integrationcontract.DecodeAnalysisPublication}
	path := integrationPath + "/" + url.PathEscape(store.request.ID) + "/analysis?issuer=" + url.QueryEscape(store.request.Issuer)

	return store, options, path
}

func TestAnalysisReadHTTPGuardsAndOriginalBytes(t *testing.T) {
	codes := map[int]string{400: "invalid_request", 401: "unauthenticated", 403: "forbidden", 405: "invalid_request", 503: "unavailable"}
	for _, mode := range []string{"success", "bearer", "duplicate-bearer", "permission", "duplicate-query", "unknown-query", "missing-query", "blank", "oversize", "body", "method", "store", "corrupt", "scope", "wrong-request", "auth-invalid", "auth-unavailable", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, options, path := analysisReadFixture(t)
			identity := Identity{Principal: store.principal, ReadRun: true}
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Authorization", "Bearer test")
			status, calls := 200, 1
			var authErr error
			switch mode {
			case "bearer":
				r.Header.Del("Authorization")
				status, calls = 401, 0
			case "duplicate-bearer":
				r.Header.Add("Authorization", "Bearer other")
				status, calls = 401, 0
			case "permission":
				identity.ReadRun = false
				identity.CreateRun = true
				status, calls = 403, 0
			case "duplicate-query":
				r.URL.RawQuery += "&issuer=urn:other"
				status, calls = 400, 0
			case "unknown-query":
				r.URL.RawQuery += "&latest=true"
				status, calls = 400, 0
			case "missing-query":
				r.URL.RawQuery = ""
				status, calls = 400, 0
			case "blank":
				r = httptest.NewRequest("GET", integrationPath+"/%20/analysis?"+r.URL.RawQuery, nil)
				r.Header.Set("Authorization", "Bearer test")
				status, calls = 400, 0
			case "oversize":
				r = httptest.NewRequest("GET", integrationPath+"/"+strings.Repeat("x", 257)+"/analysis?"+r.URL.RawQuery, nil)
				r.Header.Set("Authorization", "Bearer test")
				status, calls = 400, 0
			case "body":
				r = httptest.NewRequest("GET", path, strings.NewReader("x"))
				r.Header.Set("Authorization", "Bearer test")
				status, calls = 400, 0
			case "method":
				r.Method = http.MethodPost
				identity.CreateRun = true
				status, calls = 405, 0
			case "store":
				store.err = errors.New("private storage credentials")
				status = 503
			case "corrupt":
				store.raw = []byte(`{"private":"secret"}`)
				status = 503
			case "scope":
				options.PrincipalIssuer = "https://other.example"
				status = 503
			case "auth-unavailable":
				authErr = run.ErrUnavailable
				status, calls = 503, 0
			case "auth-invalid":
				authErr = errors.New("private credential diagnostics")
				status, calls = 401, 0
			case "wrong-request":
				message, err := integrationcontract.DecodeAnalysisPublication(store.raw)
				if err != nil {
					t.Fatal(err)
				}
				message.Data.Context.Request.ID = "another-request"
				store.raw, err = integrationcontract.EncodeAnalysisPublication(message)
				if err != nil {
					t.Fatal(err)
				}
				status = 503
			case "cancelled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
				status, calls = 503, 0
			}
			handler, err := NewAnalysisReadHandler(store, func(context.Context, string) (Identity, error) { return identity, authErr }, options)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != status || store.calls != calls || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("guard/status/cache", w.Code, store.calls, w.Body.String())
			}
			if status == 200 {
				if !bytes.Equal(w.Body.Bytes(), store.raw) || w.Header().Get("Content-Type") != "application/json" {
					t.Fatal("wire bytes changed")
				}
			} else if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") {
				t.Fatal("diagnostics leaked")
			}
			if status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing challenge")
			}
			if status != 200 && !strings.Contains(w.Body.String(), `"code":"`+codes[status]+`"`) {
				t.Fatal("status-specific error code differs", w.Body.String())
			}
			if status == 503 && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing retry")
			}
			if status == 405 && w.Header().Get("Allow") != "GET" {
				t.Fatal("method can mutate")
			}
		})
	}
}

func TestAnalysisReadHTTPUniformScopedAbsence(t *testing.T) {
	var firstBody []byte
	var firstHeaders http.Header
	for _, mode := range []string{"unknown", "invisible", "unpublished", "denied-store"} {
		store, options, path := analysisReadFixture(t)
		principal := store.principal
		switch mode {
		case "unknown":
			store.request.ID += "-other"
		case "invisible":
			principal = "another-owner"
		case "unpublished":
			store.err = run.ErrNotFound
		case "denied-store":
			store.err = run.ErrForbidden
		}
		handler, err := NewAnalysisReadHandler(store, func(context.Context, string) (Identity, error) {
			return Identity{Principal: principal, ReadRun: true}, nil
		}, options)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"unknown_request"`) || !strings.Contains(w.Body.String(), `"message":"Not Found"`) {
			t.Fatal("absence differs", w.Code, w.Body.String())
		}
		if firstBody == nil {
			firstBody = bytes.Clone(w.Body.Bytes())
			firstHeaders = w.Header().Clone()
		}
		if !bytes.Equal(firstBody, w.Body.Bytes()) || !reflect.DeepEqual(firstHeaders, w.Header()) {
			t.Fatal("absence leaked through body/headers")
		}
	}
}
