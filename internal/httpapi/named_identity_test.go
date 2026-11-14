package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestNamedIdentityBoundaryTrustAndFailureSemantics(t *testing.T) {
	trusted := NamedIdentity{Actor: serviceaccess.Identity{Issuer: "https://issuer.example", ID: "maya"}, Caller: serviceaccess.Identity{Issuer: "https://issuer.example", ID: "client:delegate"}, ActorKind: "HUMAN"}
	badKind := trusted
	badKind.ActorKind = "ADMIN"
	foreignCaller := trusted
	foreignCaller.Caller.Issuer = "https://foreign.example"
	cases := []struct {
		name                        string
		enabled                     bool
		headers                     []string
		value                       NamedIdentity
		err                         error
		cancel                      bool
		want, authCalls, operations int
	}{
		{name: "disabled", headers: []string{"Bearer credential-canary"}, want: 404},
		{name: "missing", enabled: true, want: 401},
		{name: "duplicate", enabled: true, headers: []string{"Bearer one", "Bearer two"}, want: 401},
		{name: "whitespace", enabled: true, headers: []string{"Bearer one two"}, want: 401},
		{name: "oversized", enabled: true, headers: []string{"Bearer " + strings.Repeat("x", 16385)}, want: 401},
		{name: "non-ASCII", enabled: true, headers: []string{"Bearer credential-\u00e9"}, want: 401},
		{name: "invalid kind", enabled: true, headers: []string{"Bearer credential-canary"}, value: badKind, want: 503, authCalls: 1},
		{name: "foreign caller", enabled: true, headers: []string{"Bearer credential-canary"}, value: foreignCaller, want: 503, authCalls: 1},
		{name: "cancelled verifier", enabled: true, headers: []string{"Bearer credential-canary"}, err: context.Canceled, want: 503, authCalls: 1},
		{name: "maximum credential", enabled: true, headers: []string{"Bearer " + strings.Repeat("x", 16384)}, value: trusted, want: 204, authCalls: 1, operations: 1},
		{name: "rejected", enabled: true, headers: []string{"Bearer credential-canary"}, value: trusted, err: ErrNamedCredentialRejected, want: 401, authCalls: 1},
		{name: "unavailable", enabled: true, headers: []string{"Bearer credential-canary"}, err: run.ErrUnavailable, want: 503, authCalls: 1},
		{name: "backend detail", enabled: true, headers: []string{"Bearer credential-canary"}, err: errors.New("private-backend-canary"), want: 503, authCalls: 1},
		{name: "bad successful identity", enabled: true, headers: []string{"Bearer credential-canary"}, want: 503, authCalls: 1},
		{name: "cancelled before work", enabled: true, headers: []string{"Bearer credential-canary"}, cancel: true, want: 503},
		{name: "verified", enabled: true, headers: []string{"bEaReR credential-canary"}, value: trusted, want: 204, authCalls: 1, operations: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, operations := 0, 0
			handler, err := NewNamedIdentityBoundary(tc.enabled, func(context.Context, string) (NamedIdentity, error) { calls++; return tc.value, tc.err }, func(w http.ResponseWriter, r *http.Request, identity NamedIdentity) {
				operations++
				if r.Header.Get("Authorization") != "" {
					t.Error("consumed credential forwarded to operation")
				}
				if identity != trusted {
					t.Error("request authority replaced signed identity")
				}
				w.WriteHeader(204)
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("POST", "/fixture-only", strings.NewReader(`{"actor":"attacker","service":"foreign"}`))
			request.Header.Set("X-Perfeng-Actor", "attacker")
			request.Header.Set("X-Perfeng-Service", "foreign")
			for _, header := range tc.headers {
				request.Header.Add("Authorization", header)
			}
			if tc.cancel {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want || calls != tc.authCalls || operations != tc.operations {
				t.Fatalf("status/work = %d/%d/%d, want %d/%d/%d", response.Code, calls, operations, tc.want, tc.authCalls, tc.operations)
			}
			if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "canary") {
				t.Fatal("unsafe response")
			}
			if tc.want == 401 && response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing challenge")
			}
			if tc.want == 503 && response.Header().Get("Retry-After") != "1" {
				t.Fatal("missing unavailable retry guidance")
			}
		})
	}
	if _, err := NewNamedIdentityBoundary(true, nil, nil); err == nil {
		t.Fatal("enabled without dependencies")
	}
	if _, err := NewNamedIdentityBoundary(false, nil, nil); err != nil {
		t.Fatal("disabled requires unused dependencies")
	}
}

func TestNamedIdentityCancellationAfterVerification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	handler, err := NewNamedIdentityBoundary(true, func(context.Context, string) (NamedIdentity, error) {
		cancel()
		return NamedIdentity{Actor: serviceaccess.Identity{Issuer: "https://issuer.example", ID: "human"}, Caller: serviceaccess.Identity{Issuer: "https://issuer.example", ID: "client:delegate"}, ActorKind: "HUMAN"}, nil
	}, func(http.ResponseWriter, *http.Request, NamedIdentity) {
		t.Error("cancelled verification reached operation")
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/fixture-only", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer credential-canary")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatal("cancelled authority was served")
	}
}
