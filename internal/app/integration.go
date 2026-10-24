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
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type integrationRegistry interface {
	RunApprover
	IntegrationNamespaces() (string, string, bool)
	ResolveIntegration(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, error)
}

func composeIntegration(api http.Handler, repository httpapi.Repository, authenticator BearerAuthenticator, approver RunApprover) (http.Handler, error) {
	source, ok := approver.(integrationRegistry)
	if !ok {
		return api, nil
	}
	provider, principalIssuer, enabled := source.IntegrationNamespaces()
	if !enabled {
		return api, nil
	}
	store, ok := repository.(integration.Repository)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	review := func(ctx context.Context, principal string, claims contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		if claims.Candidate.Source.Algorithm != "git-sha1" {
			return run.Request{}, "", integration.ErrUnsupportedRevision
		}
		request, err := source.ResolveIntegration(ctx, principal, claims, read)
		if err != nil {
			return run.Request{}, "", err
		}
		target, err := source.ApproveRun(ctx, principal, request)
		return request, target, err
	}
	handler, err := httpapi.NewIntegrationHandler(store, authenticator.Authenticate, httpapi.IntegrationOptions{Provider: provider, PrincipalIssuer: principalIssuer, Review: review, Decode: integrationcontract.DecodeIntegrationMessage, Encode: integrationcontract.EncodeIntegrationMessage})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/integration/") {
			handler.ServeHTTP(w, r)
		} else {
			api.ServeHTTP(w, r)
		}
	}), nil
}
