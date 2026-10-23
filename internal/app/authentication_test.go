package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/memory"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type authenticatorStub struct{}

func (authenticatorStub) Authenticate(
	context.Context,
	string,
) (httpapi.Identity, error) {
	return httpapi.Identity{Principal: "alice", ReadRun: true}, nil
}

type approverStub struct{}

type catalogAuthenticatorStub struct{}

func (catalogAuthenticatorStub) Authenticate(context.Context, string) (httpapi.Identity, error) {
	return httpapi.Identity{Principal: "integration", ReadCatalog: true}, nil
}

type catalogApproverStub struct{ approverStub }

func (catalogApproverStub) PerformanceCatalog(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
	return contract.CatalogProjection{Issuer: "https://provider.example", PrincipalIssuer: "https://auth.example", ProducerVersion: "1.0.0", Items: make([]map[string]any, 0)}, nil
}

func TestNewAPIHandlerComposesOptionalPerformanceCatalog(t *testing.T) {
	handler, err := NewAPIHandler(memory.New(nil), catalogAuthenticatorStub{}, catalogApproverStub{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/performance-catalog", nil)
	r.Header.Set("Authorization", "Bearer integration-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"totalItems":0`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func (approverStub) ApproveRun(context.Context, string, run.Request) (run.ExecutionTarget, error) {
	return run.ExecutionTargetKubernetes, nil
}

func TestNewAuthenticationLoadsMountedCredentialDocument(t *testing.T) {
	const token = "local-development-token"
	digest := sha256.Sum256([]byte(token))
	path := filepath.Join(t.TempDir(), "credentials.json")
	document := `{"schemaVersion":1,"credentials":[{"tokenSha256":"` +
		hex.EncodeToString(digest[:]) +
		`","principal":"local-operator","permissions":["runs:read"]}]}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	authenticator, err := NewAuthentication(AuthenticationConfig{CredentialFile: path})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := authenticator.Authenticate(context.Background(), token)
	if err != nil || identity.Principal != "local-operator" || !identity.ReadRun {
		t.Fatalf("Authenticate() = %#v, %v", identity, err)
	}
}

func TestNewAuthenticationRedactsCredentialFailures(t *testing.T) {
	secret := "raw-secret-that-must-not-escape"
	path := filepath.Join(t.TempDir(), secret+".json")
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewAuthentication(AuthenticationConfig{CredentialFile: path})
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), secret) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAPIHandlerComposesRequiredBoundaries(t *testing.T) {
	handler, err := NewAPIHandler(memory.New(nil), authenticatorStub{}, approverStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/runs/perf-20260909-120000-12345678",
		nil,
	)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestNewAPIHandlerRejectsMissingBoundaries(t *testing.T) {
	repository := memory.New(nil)
	for name, test := range map[string]struct {
		repository    httpapi.Repository
		authenticator BearerAuthenticator
		approver      RunApprover
	}{
		"repository":    {authenticator: authenticatorStub{}, approver: approverStub{}},
		"authenticator": {repository: repository, approver: approverStub{}},
		"approver":      {repository: repository, authenticator: authenticatorStub{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAPIHandler(
				test.repository,
				test.authenticator,
				test.approver,
			); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

var _ BearerAuthenticator = (*authentication.Static)(nil)
var _ NativeWorkerAuthenticator = (*authentication.Static)(nil)
