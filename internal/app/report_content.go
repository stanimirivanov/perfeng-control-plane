package app

import (
	"context"
	"net/http"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reportcontent"
)

// composeReportContent selects the authentication boundary once, before any
// request is inspected. A denial cannot switch the operation to another mode.
func composeReportContent(api http.Handler, resources *StartupResources, writeTimeout time.Duration) (http.Handler, error) {
	reader, err := reportcontent.New(resources.Database, resources.ArtifactStorage, writeTimeout)
	if err != nil {
		return nil, err
	}
	var reports http.Handler
	if named := resources.NamedReportIdentity; named != nil {
		bindings, ok := resources.Database.(httpapi.ReportContentBindings)
		if !ok || named.authenticator == nil {
			return nil, ErrInvalidConfiguration
		}
		operation, err := httpapi.NewServiceReportContentOperation(bindings, reader.Read)
		if err != nil {
			return nil, err
		}
		reports, err = httpapi.NewNamedIdentityBoundary(true, named.httpAuthenticate(true), operation)
		if err != nil {
			return nil, err
		}
	} else {
		reports, err = httpapi.NewLegacyReportContentHandler(resources.Authentication.Authenticate, reader.Read)
		if err != nil {
			return nil, err
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpapi.IsReportContentPath(r.URL.Path) {
			// Reserve up to a second of the socket write budget to deliver the
			// response even when authentication or object storage times out.
			budget := writeTimeout - min(time.Second, writeTimeout/2)
			ctx, cancel := context.WithTimeout(r.Context(), budget)
			defer cancel()
			reports.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		api.ServeHTTP(w, r)
	}), nil
}
