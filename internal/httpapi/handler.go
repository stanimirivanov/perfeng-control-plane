// Package httpapi implements the candidate run-management HTTP boundary.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// MaxBodyBytes is the hard limit applied before decoding an HTTP request body.
const MaxBodyBytes = 65536

// Identity must come from verified credentials, never request body/token bytes.
type Identity struct {
	Principal            string
	Create, Read, Cancel bool
}

// Authenticate verifies an opaque bearer token and returns stable identity and
// operation permissions. Implementations must be concurrency-safe.
type Authenticate func(context.Context, string) (Identity, error)

// Approve resolves approved immutable resources, verifies exact published-byte
// hashes, suite/profile support, candidate authorization, environment access and
// observe/inform policy mode. Return ErrValidation/ErrForbidden/ErrUnavailable.
// It is a required seam, not an implicit allowlist or a network-fetch mechanism.
type Approve func(context.Context, string, run.Request) error

// Repository contains the principal-scoped run and artifact reads exposed by
// the HTTP API together with run mutation operations.
type Repository interface {
	run.Repository
	// ListArtifacts follows run.ArtifactRepository visibility and ordering rules.
	ListArtifacts(context.Context, string, string) ([]run.Artifact, error)
}

// Handler serves the authenticated run-management HTTP contract.
type Handler struct {
	repository   Repository
	authenticate Authenticate
	approve      Approve
}

// New constructs a Handler only when all mandatory authorization and storage
// dependencies are present.
func New(repository Repository, authenticate Authenticate, approve Approve) (*Handler, error) {
	if repository == nil || authenticate == nil || approve == nil {
		return nil, errors.New("repository, authentication and resource approval are required")
	}
	return &Handler{repository, authenticate, approve}, nil
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	// Marshal before writing status so an invalid internal value cannot yield
	// a successful status with a truncated or missing response body.
	b, err := json.Marshal(body)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
func fail(w http.ResponseWriter, status int, code string) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	id := s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == 503 {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, apiError{code, http.StatusText(status), id})
}
func failRepository(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, run.ErrNotFound):
		fail(w, 404, "NOT_FOUND")
	case errors.Is(err, run.ErrConflict):
		fail(w, 409, "IDEMPOTENCY_CONFLICT")
	case errors.Is(err, run.ErrTerminal):
		fail(w, 409, "RUN_TERMINAL")
	case errors.Is(err, run.ErrValidation):
		fail(w, 422, "VALIDATION_FAILED")
	case errors.Is(err, run.ErrForbidden):
		fail(w, 403, "FORBIDDEN")
	case errors.Is(err, run.ErrUnavailable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		fail(w, 503, "UNAVAILABLE")
	default:
		fail(w, 500, "INTERNAL_ERROR")
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		fail(w, 401, "UNAUTHENTICATED")
		return
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n,") {
		fail(w, 401, "UNAUTHENTICATED")
		return
	}
	identity, err := h.authenticate(r.Context(), token)
	if errors.Is(err, run.ErrUnavailable) {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	if err != nil || identity.Principal == "" {
		fail(w, 401, "UNAUTHENTICATED")
		return
	}
	path := r.URL.Path
	if path == "/v1/runs" && r.Method == http.MethodPost {
		if !identity.Create {
			fail(w, 403, "FORBIDDEN")
			return
		}
		h.create(w, r, identity.Principal)

		return
	}
	h.serveRun(w, r, identity, path)
}

func (h *Handler) serveRun(
	w http.ResponseWriter,
	r *http.Request,
	identity Identity,
	path string,
) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/runs/"), "/")
	if !strings.HasPrefix(path, "/v1/runs/") || len(parts) > 2 {
		fail(w, 404, "NOT_FOUND")
		return
	}
	isGet := len(parts) == 1 && r.Method == http.MethodGet
	isArtifactList := len(parts) == 2 && parts[1] == "artifacts" && r.Method == http.MethodGet
	isCancel := len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost
	if !isGet && !isArtifactList && !isCancel {
		fail(w, 404, "NOT_FOUND")
		return
	}
	if ((isGet || isArtifactList) && !identity.Read) || (isCancel && !identity.Cancel) {
		fail(w, 403, "FORBIDDEN")
		return
	}
	if !contract.ValidID(parts[0]) {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) > 0 {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	if isArtifactList {
		h.listArtifacts(w, r, identity.Principal, parts[0])
		return
	}
	var result run.Run
	if isGet {
		result, err = h.repository.Get(r.Context(), identity.Principal, parts[0])
	} else {
		result, err = h.repository.Cancel(r.Context(), identity.Principal, parts[0])
	}
	if err != nil {
		failRepository(w, err)
		return
	}
	status := 200
	if isCancel && result.State == run.StateCancelling {
		status = 202
	}
	writeJSON(w, status, result)
}

type artifactCollection struct {
	Artifacts []run.Artifact `json:"artifacts"`
}

func (h *Handler) listArtifacts(
	w http.ResponseWriter,
	r *http.Request,
	principal string,
	runID string,
) {
	artifacts, err := h.repository.ListArtifacts(r.Context(), principal, runID)
	if err != nil {
		failRepository(w, err)

		return
	}
	if artifacts == nil {
		artifacts = make([]run.Artifact, 0)
	}
	writeJSON(w, http.StatusOK, artifactCollection{Artifacts: artifacts})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, principal string) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !run.ValidKey(keys[0]) {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		fail(w, 415, "BAD_REQUEST")
		return
	}
	media, parameters, err := mime.ParseMediaType(types[0])
	if err != nil || media != "application/json" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		fail(w, 415, "BAD_REQUEST")
		return
	}
	encodings := r.Header.Values("Content-Encoding")
	if len(encodings) > 1 || (len(encodings) == 1 && encodings[0] != "" && encodings[0] != "identity") {
		fail(w, 415, "BAD_REQUEST")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, 413, "BAD_REQUEST")
		return
	}
	if err != nil {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	value, err := parseJSON(b)
	if err != nil {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	if contract.ValidateCreate(value) != nil {
		fail(w, 422, "VALIDATION_FAILED")
		return
	}
	var request run.Request
	if json.Unmarshal(b, &request) != nil {
		fail(w, 422, "VALIDATION_FAILED")
		return
	}
	if err := h.approve(r.Context(), principal, request); err != nil {
		failRepository(w, err)
		return
	}
	accepted, err := h.repository.Accept(r.Context(), principal, keys[0], request)
	if err != nil {
		failRepository(w, err)
		return
	}
	w.Header().Set("Location", "/v1/runs/"+accepted.Run.ID)
	w.Header().Set("Idempotency-Key-Expires-At", accepted.ExpiresAt.Format(time.RFC3339Nano))
	writeJSON(w, 201, accepted.Run)
}
