package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
)

type integrationNamespaces interface {
	IntegrationNamespaces() (string, string, bool)
}

type integrationRegistry interface {
	ResolveIntegrationAdmission(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error)
}

func composeIntegration(api http.Handler, repository httpapi.Repository, authenticator BearerAuthenticator, approver RunApprover) (http.Handler, error) {
	namespaces, ok := approver.(integrationNamespaces)
	if !ok {
		return api, nil
	}
	provider, principalIssuer, enabled := namespaces.IntegrationNamespaces()
	if !enabled {
		return api, nil
	}
	source, ok := approver.(integrationRegistry)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	store, ok := repository.(integration.Repository)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	review := func(ctx context.Context, principal string, claims contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		if claims.Candidate.Source.Algorithm != "git-sha1" {
			return integration.AdmissionResolution{}, integration.ErrUnsupportedRevision
		}

		return source.ResolveIntegrationAdmission(ctx, principal, claims, read)
	}
	handler, err := httpapi.NewIntegrationHandler(store, authenticator.Authenticate, httpapi.IntegrationOptions{Provider: provider, PrincipalIssuer: principalIssuer, ResolvedReview: review, Decode: integrationcontract.DecodeIntegrationMessage, Encode: integrationcontract.EncodeIntegrationMessage})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	reader, ok := repository.(httpapi.AnalysisReadStore)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	analysis, err := httpapi.NewAnalysisReadHandler(reader, authenticator.Authenticate, httpapi.AnalysisReadOptions{Provider: provider, PrincipalIssuer: principalIssuer, Decode: integrationcontract.DecodeAnalysisPublication})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/integration/") {
			if strings.HasSuffix(r.URL.EscapedPath(), "/analysis") {
				analysis.ServeHTTP(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		} else {
			api.ServeHTTP(w, r)
		}
	}), nil
}
