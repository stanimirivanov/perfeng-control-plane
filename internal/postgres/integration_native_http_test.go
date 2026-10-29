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
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationHTTPResolvedAdmissionRetainsNativeBinding(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	message := integrationRequest(t, r, "native-http")
	message.Data.Context.Baseline = nil
	request := testRequest(t)
	calls := 0
	review := func(_ context.Context, _ string, claims contract.IntegrationContext, _ func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		calls++
		if claims.Candidate.Source.Algorithm != "git-sha1" {
			return integration.AdmissionResolution{}, integration.ErrUnsupportedRevision
		}

		return integration.AdmissionResolution{Request: request, Target: run.ExecutionTargetKubernetes, Workload: rawresult.Identity{ID: "explicit-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}, nil
	}
	options := httpapi.IntegrationOptions{Provider: "https://provider.example/identity", PrincipalIssuer: message.Data.Context.Principal.Issuer, ResolvedReview: review, Decode: integrationcontract.DecodeIntegrationMessage, Encode: integrationcontract.EncodeIntegrationMessage}
	h, err := httpapi.NewIntegrationHandler(r, func(_ context.Context, token string) (httpapi.Identity, error) {
		return httpapi.Identity{Principal: token, CreateRun: true, ReadRun: true, CancelRun: true}, nil
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
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
	first, again := send("POST", "/integration/v1/requests", "alice", body), send("POST", "/integration/v1/requests", "alice", body)
	if first.Code != 202 || first.Body.String() != again.Body.String() || calls != 1 {
		t.Fatal("resolved HTTP replay failed", first.Code, again.Code, calls)
	}
	retained, err := r.GetIntegration(testContext, "alice", message.Data.Context.Request)
	if err != nil || retained.Native == nil || retained.Native.Baseline != nil || retained.Native.Workload.ID != "explicit-workload" {
		t.Fatal("HTTP admission discarded native binding", err)
	}
	path := "/integration/v1/requests/" + url.PathEscape(message.Data.Context.Request.ID) + "?issuer=" + url.QueryEscape(message.Data.Context.Request.Issuer)
	if got := send("GET", path, "bob", nil); got.Code != 404 {
		t.Fatal("native admission weakened lookup scope", got.Code)
	}
	got := send("GET", path, "alice", nil)
	if _, err := options.Decode(got.Body.Bytes()); got.Code != 200 || err != nil {
		t.Fatal("native metadata changed lifecycle wire shape", got.Code, err)
	}
	cancel := message
	cancel.ID += "-cancel"
	cancel.Type, cancel.Data.Reason = "dev.perfeng.integration.cancel-requested.v1", "user"
	body, err = options.Encode(cancel)
	if err != nil {
		t.Fatal(err)
	}
	got = send("POST", strings.Replace(path, "?issuer=", "/cancellation?issuer=", 1), "alice", body)
	terminal, err := options.Decode(got.Body.Bytes())
	if got.Code != 200 || err != nil || terminal.Data.State != "CANCELLED" {
		t.Fatal("native admission broke cancellation", got.Code, err)
	}
	retained, err = r.GetIntegration(testContext, "alice", message.Data.Context.Request)
	if err != nil || retained.Native == nil {
		t.Fatal("cancellation lost native binding", err)
	}
	unsupported := integrationRequest(t, r, "native-http-unsupported")
	unsupported.Data.Context.Candidate.Source.Algorithm = "git-sha256"
	unsupported.Data.Context.Candidate.Source.Digest = strings.Repeat("a", 64)
	body, err = options.Encode(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	got = send("POST", "/integration/v1/requests", "alice", body)
	rejected, err := options.Decode(got.Body.Bytes())
	if got.Code != 422 || err != nil || rejected.Data.Error == nil || rejected.Data.Error.Code != "unsupported_revision" {
		t.Fatal("unsupported source classification changed", got.Code, err)
	}
}
