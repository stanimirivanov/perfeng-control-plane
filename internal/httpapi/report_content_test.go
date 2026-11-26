package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

const (
	reportRun  = "perf-20260902-130000-a1b2c3d5"
	reportID   = "88888888-8888-4888-8888-888888888888"
	reportPath = "/v1/runs/" + reportRun + "/artifacts/" + reportID + "/report-content"
)

func TestOwnedReportWire(t *testing.T) {
	api, _ := setup(t)
	var fixture struct {
		Body    string
		Headers map[string]string
	}
	example, err := contract.ReportContentFiles.ReadFile("report-content/tests/fixtures/report-content/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(example, &fixture); err != nil {
		t.Fatal(err)
	}
	body, err := contract.ReportContentFiles.ReadFile("report-content/" + fixture.Body)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewLegacyReportContentHandler(api.authenticate, func(_ context.Context, principal, runID, artifactID string) ([]byte, error) {
		if principal != "alice" || runID != reportRun || artifactID != reportID {
			t.Fatal("wrong report selection")
		}
		return body, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"alice-token", "rotated-token"} {
		response := call(handler, http.MethodGet, reportPath, token, nil)
		if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), body) {
			t.Fatalf("status %d body %q", response.Code, response.Body)
		}
		for name, want := range fixture.Headers {
			if got := response.Header().Values(name); len(got) != 1 || got[0] != want {
				t.Errorf("%s: got %v want %q", name, got, want)
			}
		}
		for _, name := range []string{"Content-Encoding", "Content-Range", "Location", "Content-Location", "ETag"} {
			if response.Header().Values(name) != nil {
				t.Errorf("unexpected %s", name)
			}
		}
	}
}

func TestReportRejectsInvalidRequestBeforeLookup(t *testing.T) {
	api, _ := setup(t)
	handler, err := NewLegacyReportContentHandler(api.authenticate, func(context.Context, string, string, string) ([]byte, error) {
		t.Error("invalid request reached evidence read")
		return nil, run.ErrNotFound
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path, header, body string }{
		{"run syntax", strings.Replace(reportPath, reportRun, "bad", 1), "", ""},
		{"artifact syntax", strings.Replace(reportPath, reportID, "bad", 1), "", ""},
		{"query", reportPath + "?uri=s3://hidden", "", ""},
		{"body", reportPath, "", "payload"},
		{"range", reportPath, "Range", ""},
		{"if-match", reportPath, "If-Match", ""},
		{"if-none-match", reportPath, "If-None-Match", ""},
		{"if-modified-since", reportPath, "If-Modified-Since", ""},
		{"if-unmodified-since", reportPath, "If-Unmodified-Since", ""},
		{"if-range", reportPath, "If-Range", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer alice-token")
			if tc.header != "" {
				request.Header.Set(tc.header, "")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertReportError(t, response, 400, "BAD_REQUEST")
		})
	}
}

func assertReportError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) string {
	t.Helper()
	var message apiError
	if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || message.Code != code || message.Message != http.StatusText(status) || message.RequestID == "" {
		t.Fatalf("got %d %s; want %d %s", response.Code, response.Body, status, code)
	}
	for _, name := range []string{"Content-Digest", "X-Perfeng-Run-Id", "X-Perfeng-Artifact-Id", "X-Perfeng-Evidence-Format", "Content-Location", "Location"} {
		if response.Header().Values(name) != nil {
			t.Fatalf("unexpected %s", name)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing error cache or sniffing protection")
	}
	return message.RequestID
}

type reportBindingLookup func(context.Context, serviceaccess.Identity, string) (serviceaccess.Binding, error)

func (lookup reportBindingLookup) GetServiceRunBinding(ctx context.Context, actor serviceaccess.Identity, runID string) (serviceaccess.Binding, error) {
	return lookup(ctx, actor, runID)
}

func TestServiceReportUsesCurrentAuthority(t *testing.T) {
	type actor struct{ Issuer, ID, Kind string }
	type authority struct {
		Actor                    actor
		GrantService             serviceaccess.Identity
		Permissions              []string
		CallerAuthorizedForActor bool
	}
	var fixture struct {
		Run struct {
			Service   serviceaccess.Identity
			CreatedBy actor
		}
		Authority      authority
		AuthorityCases []struct {
			Name     string
			Override json.RawMessage
			Status   int
		}
	}
	raw, err := contract.ReportContentFiles.ReadFile("report-content/tests/fixtures/report-content/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	current := fixture.Authority
	reportReads := 0
	bindings := reportBindingLookup(func(_ context.Context, verified serviceaccess.Identity, runID string) (serviceaccess.Binding, error) {
		if verified != (serviceaccess.Identity{Issuer: current.Actor.Issuer, ID: current.Actor.ID}) {
			t.Fatal("binding lookup used unverified actor")
		}
		if runID != reportRun || current.GrantService != fixture.Run.Service || !slices.Contains(current.Permissions, "run:read") {
			return serviceaccess.Binding{}, run.ErrNotFound
		}
		creator := fixture.Run.CreatedBy
		return serviceaccess.Binding{Service: fixture.Run.Service, Creator: serviceaccess.Identity{Issuer: creator.Issuer, ID: creator.ID}, CreatorKind: creator.Kind, OwnerPrincipal: "original-owner"}, nil
	})
	op, err := NewServiceReportContentOperation(bindings, func(_ context.Context, owner, runID, artifactID string) ([]byte, error) {
		reportReads++
		if owner != "original-owner" || runID != reportRun || artifactID != reportID {
			t.Fatal("wrong report association")
		}
		return []byte("original bytes"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewNamedIdentityBoundary(true, func(_ context.Context, token string) (NamedIdentity, error) {
		if token != "current-token" {
			return NamedIdentity{}, ErrNamedCredentialRejected
		}
		if !current.CallerAuthorizedForActor {
			return NamedIdentity{}, ErrReportScopeNotFound
		}
		return NamedIdentity{Actor: serviceaccess.Identity{Issuer: current.Actor.Issuer, ID: current.Actor.ID}, ActorKind: current.Actor.Kind, Caller: serviceaccess.Identity{Issuer: current.Actor.Issuer, ID: "client:console"}}, nil
	}, op)
	if err != nil {
		t.Fatal(err)
	}
	req := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, reportPath, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Perfeng-Actor", fixture.Run.CreatedBy.ID)
		r.Header.Set("X-Perfeng-Principal", "original-owner")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if response := req("current-token"); response.Code != 200 || reportReads != 1 {
		t.Fatal("creator with grant and delegation cannot read")
	}
	seen := map[string]bool{}
	for _, tc := range fixture.AuthorityCases {
		t.Run(tc.Name, func(t *testing.T) {
			current = fixture.Authority
			if err := json.Unmarshal(tc.Override, &current); err != nil {
				t.Fatal(err)
			}
			id := assertReportError(t, req("current-token"), tc.Status, "NOT_FOUND")
			if seen[id] || reportReads != 1 {
				t.Fatal("scope failure reused a request ID or reached evidence read")
			}
			seen[id] = true
		})
	}
	assertReportError(t, req("alice-token"), 401, "UNAUTHENTICATED")
}

func TestLegacyReportFailures(t *testing.T) {
	api, _ := setup(t)
	for _, tc := range []struct {
		name, token, path string
		readErr           error
		cancel            bool
		status            int
		code              string
	}{
		{"missing credential", "", reportPath, nil, false, 401, "UNAUTHENTICATED"},
		{"revoked credential", "revoked", reportPath, nil, false, 401, "UNAUTHENTICATED"},
		{"auth unavailable", "unavailable", reportPath, nil, false, 503, "UNAVAILABLE"},
		{"global denial", "create-only", reportPath, nil, false, 403, "FORBIDDEN"},
		{"second principal", "bob-token", reportPath, run.ErrNotFound, false, 404, "NOT_FOUND"},
		{"unknown run", "alice-token", strings.Replace(reportPath, reportRun, "perf-20260902-130000-ffffffff", 1), run.ErrNotFound, false, 404, "NOT_FOUND"},
		{"ineligible artifact", "alice-token", reportPath, run.ErrNotFound, false, 404, "NOT_FOUND"},
		{"advertised overflow", "alice-token", reportPath, run.ErrValidation, false, 422, "VALIDATION_FAILED"},
		{"corrupt object", "alice-token", reportPath, run.ErrUnavailable, false, 503, "UNAVAILABLE"},
		{"cancelled before write", "alice-token", reportPath, nil, true, 503, "UNAVAILABLE"},
		{"unexpected failure", "alice-token", reportPath, errors.New("s3://secret"), false, 500, "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			handler, err := NewLegacyReportContentHandler(api.authenticate, func(_ context.Context, principal, runID, artifactID string) ([]byte, error) {
				if tc.readErr == nil && !tc.cancel {
					t.Fatal("unauthorized evidence lookup")
				}
				if tc.token == "bob-token" && principal != "bob" {
					t.Fatal("second principal gained owner authority")
				}
				if tc.cancel {
					cancel()
				}
				return []byte("SECRET-STORAGE-FRAGMENT"), tc.readErr
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, tc.path, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			assertReportError(t, w, tc.status, tc.code)
			if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "s3://") {
				t.Fatal("private storage detail escaped")
			}
		})
	}
}
