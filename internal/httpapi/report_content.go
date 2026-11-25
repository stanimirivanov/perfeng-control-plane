package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// ReportContentRead returns owned, fully verified bytes of a directly registered
// Analysis Report. Unknown or ineligible associations return run.ErrNotFound,
// advertised overflow run.ErrValidation and verification failure run.ErrUnavailable.
type ReportContentRead func(ctx context.Context, principal, runID, artifactID string) ([]byte, error)

// NewLegacyReportContentHandler authenticates the credential on every read and
// scopes the registration lookup to the verified principal.
func NewLegacyReportContentHandler(authenticate Authenticate, read ReportContentRead) (http.Handler, error) {
	if authenticate == nil || read == nil {
		return nil, errors.New("report authentication and reader required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		identity, err := authenticate(r.Context(), token)
		if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || r.Context().Err() != nil {
			fail(w, 503, "UNAVAILABLE")
			return
		}
		if err != nil || identity.Principal == "" {
			fail(w, 401, "UNAUTHENTICATED")
			return
		}
		if !identity.ReadRun {
			fail(w, 403, "FORBIDDEN")
			return
		}
		runID, artifactID, ok := reportRequest(w, r)
		if ok {
			writeReportContent(w, r, read, identity.Principal, runID, artifactID)
		}
	}), nil
}

// ReportContentBindings exposes immutable Run ownership to a currently
// authorized actor. Missing, unbound and revoked reads all return run.ErrNotFound.
type ReportContentBindings interface {
	// GetServiceRunBinding requires the actor's current run:read grant on the Run's service.
	GetServiceRunBinding(context.Context, serviceaccess.Identity, string) (serviceaccess.Binding, error)
}

// NewServiceReportContentOperation checks the retained creator on every request.
// Its named identity boundary must have verified current caller delegation.
func NewServiceReportContentOperation(bindings ReportContentBindings, read ReportContentRead) (NamedOperation, error) {
	if bindings == nil || read == nil {
		return nil, errors.New("report service binding and reader required")
	}
	return func(w http.ResponseWriter, r *http.Request, identity NamedIdentity) {
		runID, artifactID, ok := reportRequest(w, r)
		if !ok {
			return
		}
		binding, err := bindings.GetServiceRunBinding(r.Context(), identity.Actor, runID)
		if err != nil {
			failRepository(w, err)
			return
		}
		if binding.Creator != identity.Actor || binding.CreatorKind != identity.ActorKind {
			fail(w, 404, "NOT_FOUND")
			return
		}
		if binding.OwnerPrincipal == "" || !binding.Service.Valid() {
			fail(w, 503, "UNAVAILABLE")
			return
		}
		writeReportContent(w, r, read, binding.OwnerPrincipal, runID, artifactID)
	}, nil
}

// IsReportContentPath recognizes the report operation before credential mode
// selection, including routes containing an invalid identifier.
func IsReportContentPath(path string) bool {
	parts := strings.SplitN(path, "/", 8)
	return len(parts) == 7 && parts[0] == "" && parts[1] == "v1" && parts[2] == "runs" && parts[4] == "artifacts" && parts[6] == "report-content"
}

var reportArtifactID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func reportRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if r.Method != http.MethodGet || !IsReportContentPath(r.URL.Path) {
		fail(w, 404, "NOT_FOUND")
		return "", "", false
	}
	parts := strings.Split(r.URL.Path, "/")
	if !contract.ValidID(parts[3]) || !reportArtifactID.MatchString(parts[5]) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		fail(w, 400, "BAD_REQUEST")
		return "", "", false
	}
	for _, header := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		if len(r.Header.Values(header)) != 0 {
			fail(w, 400, "BAD_REQUEST")
			return "", "", false
		}
	}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if len(body) != 0 || err != nil {
			fail(w, 400, "BAD_REQUEST")
			return "", "", false
		}
	}
	return parts[3], parts[5], true
}

func writeReportContent(w http.ResponseWriter, r *http.Request, read ReportContentRead, principal, runID, artifactID string) {
	body, err := read(r.Context(), principal, runID, artifactID)
	if r.Context().Err() != nil {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	if err != nil {
		failRepository(w, err)
		return
	}
	digest := sha256.Sum256(body)
	headers := w.Header()
	headers.Set("Content-Type", "application/octet-stream")
	headers.Set("Content-Length", strconv.Itoa(len(body)))
	headers.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
	headers.Set("Cache-Control", "no-store")
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("X-Perfeng-Evidence-Media-Type", "application/json")
	headers.Set("X-Perfeng-Evidence-Format", "analysis-result/v1")
	headers.Set("X-Perfeng-Run-Id", runID)
	headers.Set("X-Perfeng-Artifact-Id", artifactID)
	headers.Set("X-Perfeng-Artifact-Run-Id", runID)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		return // A transport failure after headers cannot be replaced with a JSON error.
	}
}
