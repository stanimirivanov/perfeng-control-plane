package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
)

type nativeIntegrationStoreStub struct {
	*integrationStoreStub
	nativeCalls int
}

func (s *nativeIntegrationStoreStub) AdmitIntegrationResolved(ctx context.Context, principal, provider string, message contract.IntegrationMessage, _ integration.ResolvedReview) (integration.Record, error) {
	s.nativeCalls++
	return s.integrationStoreStub.AdmitIntegration(ctx, principal, provider, message, nil)
}

func TestIntegrationResolvedModeRequiresExclusiveReviewAndNativeStorage(t *testing.T) {
	_, store, options := integrationHTTPFixture(t)
	resolved := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		return integration.AdmissionResolution{}, nil
	}
	auth := func(context.Context, string) (Identity, error) { return Identity{Principal: "alice"}, nil }
	options.ResolvedReview = resolved
	if _, err := NewIntegrationHandler(store, auth, options); err == nil {
		t.Fatal("ambiguous review mode accepted")
	}
	options.Review = nil
	if _, err := NewIntegrationHandler(store, auth, options); err == nil {
		t.Fatal("resolved review accepted legacy-only storage")
	}
	if _, err := NewIntegrationHandler(&nativeIntegrationStoreStub{integrationStoreStub: store}, auth, options); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationResolvedAdmissionKeepsAuthenticationAndScopeGates(t *testing.T) {
	for _, test := range []struct {
		name                     string
		legacy, noBearer, denied bool
		issuer, principal        string
		status, nativeCalls      int
	}{
		{name: "allowed", status: 202, nativeCalls: 1},
		{name: "legacy", legacy: true, status: 202},
		{name: "no bearer", noBearer: true, status: 401},
		{name: "no permission", denied: true, status: 403},
		{name: "wrong issuer", issuer: "https://other.example", status: 403},
		{name: "wrong principal", principal: "bob", status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, legacy, options := integrationHTTPFixture(t)
			store := &nativeIntegrationStoreStub{integrationStoreStub: legacy}
			legacyReview := options.Review
			options.Review = nil
			options.ResolvedReview = func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
				return integration.AdmissionResolution{}, nil
			}
			if test.legacy {
				options.Review, options.ResolvedReview = legacyReview, nil
			}
			h, err := NewIntegrationHandler(store, func(context.Context, string) (Identity, error) {
				return Identity{Principal: "alice", CreateRun: !test.denied}, nil
			}, options)
			if err != nil {
				t.Fatal(err)
			}
			if test.issuer != "" {
				message.Data.Context.Principal.Issuer = test.issuer
			}
			if test.principal != "" {
				message.Data.Context.Principal.ID = test.principal
			}
			raw, err := options.Encode(message)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", integrationPath, strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			if !test.noBearer {
				r.Header.Set("Authorization", "Bearer test")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status || store.nativeCalls != test.nativeCalls {
				t.Fatal("native mode bypassed scope/auth gates", w.Code, store.nativeCalls)
			}
		})
	}
}
