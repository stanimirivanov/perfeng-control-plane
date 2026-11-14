package postgres_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
)

// This path is a private test callback, not a new public API or mounted route.
func requireKeycloakHTTP(t *testing.T, ctx context.Context, runtime *app.NamedIdentityAuthentication, token string, status int, expected authentication.VerifiedIdentity) {
	t.Helper()
	operations := 0
	handler, err := app.NewNamedIdentityBoundary(true, runtime, func(w http.ResponseWriter, r *http.Request, identity httpapi.NamedIdentity) {
		operations++
		if identity.Actor != expected.Actor || identity.Caller != expected.Caller || identity.ActorKind != expected.ActorKind {
			t.Error("HTTP operation authority differs from signed expectation")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err != nil {
		t.Fatal("cannot bind qualified HTTP verifier")
	}
	request := httptest.NewRequest("POST", "https://fixture.invalid/identity-boundary-only", strings.NewReader(`{"actor":"forged","service":"foreign"}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Perfeng-Actor", "forged")
	request.Header.Set("X-Perfeng-Service", "foreign")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status || (status != 204 && operations != 0) || (status == 204 && operations != 1) {
		t.Fatalf("real HTTP status/work mismatch: %d/%d", response.Code, operations)
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), token) {
		t.Fatal("unsafe real HTTP response")
	}
}
