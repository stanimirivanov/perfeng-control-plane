package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const integrationPath = "/integration/v1/requests"

// IntegrationOptions contains trusted startup namespaces and composition-injected
// wire validation. It grants no authority to fields supplied by the caller.
type IntegrationOptions struct {
	Provider, PrincipalIssuer string
	Review                    integration.Review
	// ResolvedReview selects atomic native capture, requiring NativeRepository.
	// Supply exactly one of Review or ResolvedReview; reads/cancel use the same store.
	ResolvedReview integration.ResolvedReview
	Decode         func([]byte) (contract.IntegrationMessage, error)
	Encode         func(contract.IntegrationMessage) ([]byte, error)
}

type integrationHandler struct {
	store        integration.Repository
	authenticate Authenticate
	options      IntegrationOptions
	native       integration.NativeRepository
}

// NewIntegrationHandler exposes admission, scoped reads and cancellation. Codec injection
// keeps vendor schema adapters out of this adapter's dependencies. Dependencies
// are immutable and concurrent-safe; a durable store is mandatory.
func NewIntegrationHandler(store integration.Repository, authenticate Authenticate, options IntegrationOptions) (http.Handler, error) {
	if store == nil || authenticate == nil || (options.Review == nil) == (options.ResolvedReview == nil) || options.Decode == nil || options.Encode == nil {
		return nil, errors.New("integration dependencies required")
	}
	var native integration.NativeRepository
	if options.ResolvedReview != nil {
		var ok bool
		native, ok = store.(integration.NativeRepository)
		if !ok {
			return nil, errors.New("native integration storage required")
		}
	}
	for _, issuer := range []string{options.Provider, options.PrincipalIssuer} {
		parsed, err := url.Parse(issuer)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(issuer) > 512 {
			return nil, errors.New("invalid integration namespace")
		}
	}

	return &integrationHandler{store: store, authenticate: authenticate, options: options, native: native}, nil
}

func integrationFailure(w http.ResponseWriter, status int, code string) {
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, contract.IntegrationError{Code: code, Message: http.StatusText(status)})
}

func (h *integrationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok {
		integrationFailure(w, 401, "unauthenticated")
		return
	}
	identity, err := h.authenticate(r.Context(), token)
	if err != nil || identity.Principal == "" {
		if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			integrationFailure(w, 503, "unavailable")
		} else {
			integrationFailure(w, 401, "unauthenticated")
		}
		return
	}
	var result integration.Record
	status := 200
	isCancel := r.Method == http.MethodPost && strings.HasPrefix(r.URL.EscapedPath(), integrationPath+"/") && strings.HasSuffix(r.URL.EscapedPath(), "/cancellation")
	switch {
	case r.URL.Path == integrationPath && r.Method == http.MethodPost || isCancel:
		if !isCancel && !identity.CreateRun || isCancel && !identity.CancelRun {
			integrationFailure(w, 403, "forbidden")
			return
		}
		if !isCancel && r.URL.RawQuery != "" {
			integrationFailure(w, 400, "invalid_request")
			return
		}
		media, params, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if len(r.Header.Values("Content-Type")) != 1 || mediaErr != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
			integrationFailure(w, 415, "invalid_request")
			return
		}
		if r.Header.Get("Content-Encoding") != "" {
			integrationFailure(w, 415, "invalid_request")
			return
		}
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
		if readErr != nil || len(raw) > MaxBodyBytes {
			integrationFailure(w, 400, "invalid_request")
			return
		}
		message, decodeErr := h.options.Decode(raw)
		kind := "dev.perfeng.integration.requested.v1"
		if isCancel {
			kind = "dev.perfeng.integration.cancel-requested.v1"
		}
		if decodeErr != nil || message.Type != kind {
			code := "invalid_request"
			if errors.Is(decodeErr, contract.ErrIntegrationVersion) {
				code = "unsupported_version"
			}
			integrationFailure(w, 400, code)
			return
		}
		if message.Data.Context.Principal != (contract.CatalogIdentity{Issuer: h.options.PrincipalIssuer, ID: identity.Principal}) {
			integrationFailure(w, 403, "forbidden")
			return
		}
		if isCancel {
			lookup, valid := integrationLookup(strings.TrimSuffix(r.URL.EscapedPath(), "/cancellation"), r.URL.RawQuery)
			if !valid || lookup != message.Data.Context.Request {
				integrationFailure(w, 400, "invalid_request")
				return
			}
			result, err = h.store.CancelIntegration(r.Context(), identity.Principal, message)
		} else if h.native != nil {
			result, err = h.native.AdmitIntegrationResolved(r.Context(), identity.Principal, h.options.Provider, message, h.options.ResolvedReview)
		} else {
			result, err = h.store.AdmitIntegration(r.Context(), identity.Principal, h.options.Provider, message, h.options.Review)
		}
		status = 202
	case strings.HasPrefix(r.URL.Path, integrationPath+"/") && r.Method == http.MethodGet:
		if !identity.ReadRun {
			integrationFailure(w, 403, "forbidden")
			return
		}
		lookup, valid := integrationLookup(r.URL.EscapedPath(), r.URL.RawQuery)
		body, bodyErr := io.ReadAll(io.LimitReader(r.Body, 1))
		if !valid || bodyErr != nil || len(body) != 0 {
			integrationFailure(w, 400, "invalid_request")
			return
		}
		result, err = h.store.GetIntegration(r.Context(), identity.Principal, lookup)
	default:
		integrationFailure(w, 404, "unknown_request")
		return
	}
	if err != nil {
		status, code := 503, "unavailable"
		switch {
		case errors.Is(err, run.ErrConflict):
			status, code = 409, "conflict"
		case errors.Is(err, run.ErrNotFound):
			status, code = 404, "unknown_request"
		case errors.Is(err, run.ErrForbidden):
			status, code = 403, "forbidden"
		case errors.Is(err, run.ErrValidation):
			status, code = 400, "invalid_request"
		}
		integrationFailure(w, status, code)
		return
	}
	message := result.Disposition
	if message.Source != h.options.Provider || message.Data.Context.Principal != (contract.CatalogIdentity{Issuer: h.options.PrincipalIssuer, ID: identity.Principal}) {
		integrationFailure(w, 503, "unavailable")
		return
	}
	if message.Type == "dev.perfeng.integration.rejected.v1" {
		status = 422
	} else {
		if message.Type != "dev.perfeng.integration.accepted.v1" || result.Run == nil || message.Data.Run == nil || result.Run.ID != message.Data.Run.ID || message.Data.Run.Issuer != h.options.Provider {
			integrationFailure(w, 503, "unavailable")
			return
		}
		if r.Method == http.MethodGet || isCancel {
			message = integrationSnapshot(message, *result.Run)
			if isCancel {
				status = 200
				if message.Data.State == "CANCELLING" {
					status = 202
				} else if !contract.Terminal(string(result.Run.State)) {
					integrationFailure(w, 503, "unavailable")
					return
				}
			}
		}
	}
	raw, err := h.options.Encode(message)
	if err != nil {
		integrationFailure(w, 503, "unavailable")
		return
	}
	writeJSON(w, status, json.RawMessage(raw))
}

func integrationLookup(path, rawQuery string) (contract.CatalogIdentity, bool) {
	encodedID := strings.TrimPrefix(path, integrationPath+"/")
	id, idErr := url.PathUnescape(encodedID)
	query, queryErr := url.ParseQuery(rawQuery)
	issuer := query.Get("issuer")
	parsed, issuerErr := url.Parse(issuer)
	valid := idErr == nil && !strings.Contains(encodedID, "/") && strings.TrimSpace(id) != "" && utf8.ValidString(id) && utf8.RuneCountInString(id) <= 256 && !strings.ContainsRune(id, 0) && queryErr == nil && len(query) == 1 && len(query["issuer"]) == 1 && issuerErr == nil && parsed.IsAbs() && len(issuer) <= 512 && !strings.ContainsRune(issuer, 0)
	return contract.CatalogIdentity{Issuer: issuer, ID: id}, valid
}

func integrationSnapshot(message contract.IntegrationMessage, current run.Run) contract.IntegrationMessage {
	states := map[run.State]string{run.StateCreated: "ACCEPTED", run.StateValidating: "ACCEPTED", run.StateProvisioning: "ACCEPTED", run.StateWarmingUp: "ACCEPTED", run.StateRunning: "RUNNING", run.StateCollecting: "RUNNING", run.StateAnalyzing: "RUNNING", run.StateReporting: "RUNNING", run.StateCancelling: "CANCELLING", run.StateCompleted: "COMPLETED", run.StateInvalid: "FAILED", run.StateInfrastructureFailure: "FAILED", run.StateTestFailure: "FAILED", run.StateAborted: "CANCELLED"}
	message.Type = "dev.perfeng.integration.lifecycle.v1"
	message.Time = current.UpdatedAt.UTC().Format(time.RFC3339Nano)
	revision := json.Number(strconv.FormatInt(current.Revision, 10))
	message.Data.Revision = &revision
	message.Data.State = states[current.State]
	key, _ := json.Marshal([]string{message.Source, current.ID, string(revision)})
	digest := sha256.Sum256(key)
	message.ID = "snapshot-" + hex.EncodeToString(digest[:])
	return message
}
