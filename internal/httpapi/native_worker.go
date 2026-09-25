package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	nativeWorkerPresencePath     = "/native-worker/v1/presence"
	nativeWorkerHeartbeat        = 30
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
}

// NewNativeWorkerHandler constructs the worker HTTP boundary from mandatory
// authentication and durable storage dependencies.
func NewNativeWorkerHandler(
	store NativeWorkerPresenceStore,
	authenticate NativeWorkerAuthenticate,
) (*NativeWorkerHandler, error) {
	if store == nil || authenticate == nil {
		return nil, errors.New("native worker authentication and storage are required")
	}

	return &NativeWorkerHandler{store: store, authenticate: authenticate}, nil
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
	if r.URL.Path != nativeWorkerPresencePath || r.Method != http.MethodPost {
		fail(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		return
	}
	identity, err := handler.authenticate(r.Context(), token)
	if errors.Is(err, run.ErrUnavailable) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		fail(w, http.StatusServiceUnavailable, "UNAVAILABLE")
		return
	}
	if err != nil || !validWorkerIdentity(identity) {
		fail(w, http.StatusUnauthorized, "UNAUTHENTICATED")
		return
	}

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
