package postgres

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationHTTPUsesDurableAdmissionAndScopedSnapshots(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	message := integrationRequest(t, r, "http")
	request := testRequest(t)
	review := func(_ context.Context, _ string, claims contract.IntegrationContext, _ func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		if claims.Candidate.Source.Algorithm != "git-sha1" {
			return run.Request{}, "", integration.ErrUnsupportedRevision
		}
		return request, run.ExecutionTargetKubernetes, nil
	}
	options := httpapi.IntegrationOptions{Provider: "https://provider.example/identity", PrincipalIssuer: message.Data.Context.Principal.Issuer, Review: review, Decode: integrationcontract.DecodeIntegrationMessage, Encode: integrationcontract.EncodeIntegrationMessage}
	h, err := httpapi.NewIntegrationHandler(r, func(_ context.Context, token string) (httpapi.Identity, error) {
		return httpapi.Identity{Principal: token, CreateRun: true, ReadRun: true}, nil
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	body, err := options.Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	first, again := send("POST", "/integration/v1/requests", string(body), "alice"), send("POST", "/integration/v1/requests", string(body), "alice")
	if first.Code != 202 || first.Body.String() != again.Body.String() {
		t.Fatal("HTTP replay not durable", first.Code, again.Body.String())
	}
	path := "/integration/v1/requests/" + url.PathEscape(message.Data.Context.Request.ID) + "?issuer=" + url.QueryEscape(message.Data.Context.Request.Issuer)
	if got := send("GET", path, "", "bob"); got.Code != 404 {
		t.Fatal("HTTP scope leak", got.Code)
	}
	got := send("GET", path, "", "alice")
	snapshot, err := options.Decode(got.Body.Bytes())
	if got.Code != 200 || err != nil || snapshot.Data.State != "ACCEPTED" {
		t.Fatal("current snapshot unavailable", got.Code, err)
	}
	message = integrationRequest(t, r, "unsupported-source")
	message.Data.Context.Candidate.Source.Algorithm = "git-sha256"
	message.Data.Context.Candidate.Source.Digest = strings.Repeat("a", 64)
	body, err = options.Encode(message)
	if err != nil {
		t.Fatal(err)
	}
	rejected := send("POST", "/integration/v1/requests", string(body), "alice")
	disposition, err := options.Decode(rejected.Body.Bytes())
	if rejected.Code != 422 || err != nil || disposition.Data.Error == nil || disposition.Data.Error.Code != "unsupported_revision" {
		t.Fatal("unsupported revision not durably classified", rejected.Code, err)
	}
}
