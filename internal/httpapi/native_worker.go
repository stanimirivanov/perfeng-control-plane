package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
}

// NewNativeWorkerHandler constructs the worker HTTP boundary from mandatory
// authentication and durable storage dependencies.
func NewNativeWorkerHandler(
	store NativeWorkerPresenceStore,
	authenticate NativeWorkerAuthenticate,
	acquire NativeWorkerAcquire,
) (*NativeWorkerHandler, error) {
	if store == nil || authenticate == nil || acquire == nil {
		return nil, errors.New("native worker authentication, presence and acquisition are required")
	}

	return &NativeWorkerHandler{store: store, authenticate: authenticate, acquire: acquire}, nil
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
	if r.Method != http.MethodPost ||
		(r.URL.Path != nativeWorkerPresencePath && r.URL.Path != nativeWorkerLeasePath) {
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
	handler.acquireLease(w, r, identity)
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
		fail(w, http.StatusServiceUnavailable, "UNAVAILABLE")
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
	document, _, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	presence, err := nativeworker.ParsePresence(document, MaxBodyBytes)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
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
		failRepository(w, err)
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
		fail(w, http.StatusForbidden, "FORBIDDEN")
		return
	}
	_, value, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if contract.ValidateAcquireLease(value) != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	lease, acquired, err := handler.acquire(r.Context(), identity.WorkerID)
	if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		fail(w, http.StatusServiceUnavailable, "UNAVAILABLE")
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
