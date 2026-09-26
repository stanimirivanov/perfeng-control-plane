package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	nativeWorkerPresencePath     = "/native-worker/v1/presence"
	nativeWorkerLeasePath        = "/native-worker/v1/leases"
	nativeWorkerHeartbeat        = 30
	nativeWorkerLeaseRetryAfter  = 15
	nativeWorkerQualificationTTL = 2 * time.Minute
)

// WorkerIdentity is the server-owned principal and policy resolved from a
// credential. QualificationEvidence contains canonical reviewed JSON bytes.
type WorkerIdentity struct {
	WorkerID              string
	AuthorizedToAcquire   bool
	QualificationEvidence json.RawMessage
}

// NativeWorkerAuthenticate verifies a worker-audience bearer credential.
type NativeWorkerAuthenticate func(context.Context, string) (WorkerIdentity, error)

// NativeWorkerAcquire returns one durable compatible lease for an authenticated
// worker, or acquired=false when no eligible work is currently available.
type NativeWorkerAcquire func(context.Context, string) (nativeworker.Lease, bool, error)

// NativeWorkerRenew extends one current lease and returns authoritative
// lifecycle, timing and cancellation state.
type NativeWorkerRenew func(
	context.Context,
	string,
	nativeworker.RenewalRequest,
) (nativeworker.Renewal, error)

// NativeWorkerPresenceRecord is the complete last-seen state persisted for an
// authenticated worker. Document is an independently owned JSON value.
type NativeWorkerPresenceRecord struct {
	WorkerID            string
	ObservedDocument    json.RawMessage
	Availability        string
	Qualified           bool
	AuthorizedToAcquire bool
}

// NativeWorkerPresenceStore durably replaces a worker's last-seen presence and
// returns the authoritative database observation time.
type NativeWorkerPresenceStore interface {
	RecordNativeWorkerPresence(context.Context, NativeWorkerPresenceRecord) (time.Time, error)
}

// NativeWorkerHandler serves the independently authenticated Native Worker API.
type NativeWorkerHandler struct {
	store        NativeWorkerPresenceStore
	authenticate NativeWorkerAuthenticate
	acquire      NativeWorkerAcquire
	renew        NativeWorkerRenew
}

// NewNativeWorkerHandler constructs the worker HTTP boundary from mandatory
// authentication and durable storage dependencies.
func NewNativeWorkerHandler(
	store NativeWorkerPresenceStore,
	authenticate NativeWorkerAuthenticate,
	acquire NativeWorkerAcquire,
	renew NativeWorkerRenew,
) (*NativeWorkerHandler, error) {
	if store == nil || authenticate == nil || acquire == nil || renew == nil {
		return nil, errors.New("native worker authentication, presence, acquisition and renewal are required")
	}

	return &NativeWorkerHandler{
		store: store, authenticate: authenticate, acquire: acquire, renew: renew,
	}, nil
}

type qualificationDecision struct {
	Status     string     `json:"status"`
	Reasons    []string   `json:"reasons"`
	ValidUntil *time.Time `json:"validUntil,omitempty"`
}

type workerPresenceStatus struct {
	WorkerID            string                `json:"workerId"`
	ServerTime          time.Time             `json:"serverTime"`
	NextHeartbeat       int                   `json:"nextHeartbeatSeconds"`
	Availability        string                `json:"availability"`
	Qualification       qualificationDecision `json:"qualification"`
	AuthorizedToAcquire bool                  `json:"authorizedToAcquire"`
}

func (handler *NativeWorkerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	leaseID, renewal := nativeWorkerRenewalLeaseID(r.URL.Path)
	if r.Method != http.MethodPost || (r.URL.Path != nativeWorkerPresencePath &&
		r.URL.Path != nativeWorkerLeasePath && !renewal) {
		fail(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	identity, ok := handler.authenticateRequest(w, r)
	if !ok {
		return
	}
	if r.URL.Path == nativeWorkerPresencePath {
		handler.reportPresence(w, r, identity)
		return
	}
	if r.URL.Path == nativeWorkerLeasePath {
		handler.acquireLease(w, r, identity)
		return
	}
	handler.renewLease(w, r, identity, leaseID)
}

func (handler *NativeWorkerHandler) authenticateRequest(
	w http.ResponseWriter,
	r *http.Request,
) (WorkerIdentity, bool) {
	token, ok := bearerToken(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		return WorkerIdentity{}, false
	}
	identity, err := handler.authenticate(r.Context(), token)
	if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
		return WorkerIdentity{}, false
	}
	if err != nil || !validWorkerIdentity(identity) {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		return WorkerIdentity{}, false
	}

	return identity, true
}

func (handler *NativeWorkerHandler) reportPresence(
	w http.ResponseWriter,
	r *http.Request,
	identity WorkerIdentity,
) {
	document, _, ok := readJSONBodyWithCode(w, r, "INVALID_REQUEST")
	if !ok {
		return
	}
	presence, err := nativeworker.ParsePresence(document, MaxBodyBytes)
	if err != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	qualified := bytes.Equal(presence.QualificationEvidence, identity.QualificationEvidence)
	observedAt, err := handler.store.RecordNativeWorkerPresence(
		r.Context(),
		NativeWorkerPresenceRecord{
			WorkerID: identity.WorkerID, ObservedDocument: append(json.RawMessage(nil), document...),
			Availability: presence.Availability, Qualified: qualified,
			AuthorizedToAcquire: identity.AuthorizedToAcquire,
		},
	)
	if err != nil {
		failNativeWorkerRepository(w, err)
		return
	}
	decision := qualificationDecision{
		Status: "UNQUALIFIED", Reasons: []string{"qualification evidence is not approved"},
	}
	if qualified {
		validUntil := observedAt.Add(nativeWorkerQualificationTTL)
		decision = qualificationDecision{
			Status: "QUALIFIED", Reasons: []string{}, ValidUntil: &validUntil,
		}
	}
	writeJSON(w, http.StatusOK, workerPresenceStatus{
		WorkerID: identity.WorkerID, ServerTime: observedAt,
		NextHeartbeat: nativeWorkerHeartbeat, Availability: presence.Availability,
		Qualification: decision, AuthorizedToAcquire: identity.AuthorizedToAcquire,
	})
}

func (handler *NativeWorkerHandler) acquireLease(
	w http.ResponseWriter,
	r *http.Request,
	identity WorkerIdentity,
) {
	if !identity.AuthorizedToAcquire {
		fail(w, http.StatusForbidden, "NOT_AUTHORIZED")
		return
	}
	_, value, ok := readJSONBodyWithCode(w, r, "INVALID_REQUEST")
	if !ok {
		return
	}
	if contract.ValidateAcquireLease(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	lease, acquired, err := handler.acquire(r.Context(), identity.WorkerID)
	if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	if !acquired {
		w.Header().Set("Retry-After", strconv.Itoa(nativeWorkerLeaseRetryAfter))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNoContent)

		return
	}
	if !lease.Valid() {
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}

	writeNativeLease(w, lease)
}

type nativeRenewLeaseRequest struct {
	AttemptID           string             `json:"attemptId"`
	ExpectedRunRevision int64              `json:"expectedRunRevision"`
	Phase               nativeworker.Phase `json:"phase"`
}

func (handler *NativeWorkerHandler) renewLease(
	w http.ResponseWriter,
	r *http.Request,
	identity WorkerIdentity,
	leaseID string,
) {
	if !identity.AuthorizedToAcquire {
		fail(w, http.StatusForbidden, "NOT_AUTHORIZED")
		return
	}
	capabilities := r.Header.Values("X-Perfeng-Lease")
	if len(capabilities) != 1 || !nativeworker.ValidLeaseCapability(capabilities[0]) {
		fail(w, http.StatusForbidden, "CAPABILITY_MISMATCH")
		return
	}
	document, value, ok := readJSONBodyWithCode(w, r, "INVALID_REQUEST")
	if !ok {
		return
	}
	if contract.ValidateRenewLease(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input nativeRenewLeaseRequest
	if json.Unmarshal(document, &input) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	request := nativeworker.RenewalRequest{
		LeaseID: leaseID, AttemptID: input.AttemptID,
		ExpectedRunRevision: input.ExpectedRunRevision,
		Phase:               input.Phase, Capability: capabilities[0],
	}
	if !request.Valid() {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	renewal, err := handler.renew(r.Context(), identity.WorkerID, request)
	switch {
	case errors.Is(err, nativeworker.ErrLeaseGone):
		fail(w, http.StatusGone, "LEASE_EXPIRED")
		return
	case errors.Is(err, run.ErrRevision), errors.Is(err, run.ErrConflict),
		errors.Is(err, run.ErrTransition):
		fail(w, http.StatusConflict, "REVISION_CONFLICT")
		return
	case errors.Is(err, run.ErrValidation):
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	case errors.Is(err, run.ErrUnavailable), errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	case !renewal.Valid():
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}

	writeJSON(w, http.StatusOK, renewal)
}

func nativeWorkerRenewalLeaseID(path string) (string, bool) {
	const prefix = nativeWorkerLeasePath + "/"
	const suffix = "/renew"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	leaseID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)

	return leaseID, leaseID != "" && !strings.Contains(leaseID, "/")
}

func failNativeWorkerRepository(w http.ResponseWriter, err error) {
	if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
		return
	}
	if errors.Is(err, run.ErrForbidden) {
		fail(w, http.StatusForbidden, "NOT_AUTHORIZED")
		return
	}
	if errors.Is(err, run.ErrValidation) {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
}

func writeNativeLease(w http.ResponseWriter, lease nativeworker.Lease) {
	document, err := json.Marshal(lease)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	w.Header().Set("X-Perfeng-Lease", lease.Capability)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(append(document, '\n')); err != nil {
		return
	}
}

func validWorkerIdentity(identity WorkerIdentity) bool {
	if !contract.ValidNativeWorkerIdentifier(identity.WorkerID) || len(identity.QualificationEvidence) == 0 {
		return false
	}
	var evidence any
	if json.Unmarshal(identity.QualificationEvidence, &evidence) != nil ||
		contract.ValidateWorkerQualificationEvidence(evidence) != nil {
		return false
	}
	canonical, err := json.Marshal(evidence)

	return err == nil && bytes.Equal(canonical, identity.QualificationEvidence)
}
