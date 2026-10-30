package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// AnalysisReadStore exposes only owned principal/request-scoped retained bytes.
type AnalysisReadStore interface {
	// GetAnalysisPublication returns original bytes; unknown, invisible and
	// unpublished requests are not-found. It performs no approval or materialization.
	GetAnalysisPublication(context.Context, string, contract.CatalogIdentity) ([]byte, error)
}

// AnalysisReadOptions provides trusted namespaces and an offline closed codec.
type AnalysisReadOptions struct {
	Provider, PrincipalIssuer string
	Decode                    func([]byte) (contract.AnalysisPublication, error)
}

type analysisReadHandler struct {
	store        AnalysisReadStore
	authenticate Authenticate
	options      AnalysisReadOptions
}

// NewAnalysisReadHandler creates a read-only adapter, with no I/O at construction.
// It reuses request-read permission and returns validated original wire bytes.
func NewAnalysisReadHandler(store AnalysisReadStore, authenticate Authenticate, options AnalysisReadOptions) (http.Handler, error) {
	if store == nil || authenticate == nil || options.Decode == nil {
		return nil, errors.New("analysis read dependencies required")
	}
	for _, issuer := range []string{options.Provider, options.PrincipalIssuer} {
		parsed, err := url.Parse(issuer)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(issuer) > 512 {
			return nil, errors.New("invalid analysis read namespace")
		}
	}

	return &analysisReadHandler{store: store, authenticate: authenticate, options: options}, nil
}

func (h *analysisReadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, valid := bearerToken(r)
	if !valid {
		integrationFailure(w, 401, "unauthenticated")
		return
	}
	identity, err := h.authenticate(r.Context(), token)
	if err != nil || identity.Principal == "" || r.Context().Err() != nil {
		status, code := 401, "unauthenticated"
		if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || r.Context().Err() != nil {
			status, code = 503, "unavailable"
		}
		integrationFailure(w, status, code)

		return
	}
	if !identity.ReadRun {
		integrationFailure(w, 403, "forbidden")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		integrationFailure(w, 405, "invalid_request")
		return
	}
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, integrationPath+"/") || !strings.HasSuffix(path, "/analysis") {
		integrationFailure(w, 404, "unknown_request")
		return
	}
	lookup, valid := integrationLookup(strings.TrimSuffix(path, "/analysis"), r.URL.RawQuery)
	if !valid || r.ContentLength > 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Encoding") != "" {
		integrationFailure(w, 400, "invalid_request")
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			integrationFailure(w, 400, "invalid_request")
			return
		}
	}
	raw, err := h.store.GetAnalysisPublication(r.Context(), identity.Principal, lookup)
	if err != nil {
		status, code := 503, "unavailable"
		if errors.Is(err, run.ErrNotFound) || errors.Is(err, run.ErrForbidden) {
			status, code = 404, "unknown_request"
		}
		integrationFailure(w, status, code)

		return
	}
	message, err := h.options.Decode(raw)
	if err != nil || r.Context().Err() != nil || message.Source != h.options.Provider || message.Data.Context.Request != lookup ||
		message.Data.Context.Principal != (contract.CatalogIdentity{Issuer: h.options.PrincipalIssuer, ID: identity.Principal}) {
		integrationFailure(w, 503, "unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw); err != nil {
		return // The transport owns partial-write recovery; never append an error body.
	}
}
