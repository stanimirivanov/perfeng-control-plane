package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type nativeReleaseLeaseRequest struct {
	AttemptID           string                     `json:"attemptId"`
	ExpectedRunRevision int64                      `json:"expectedRunRevision"`
	Reason              nativeworker.ReleaseReason `json:"reason"`
	Message             string                     `json:"message,omitempty"`
}

func (handler *NativeWorkerHandler) releaseLease(
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
	if contract.ValidateReleaseLease(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input nativeReleaseLeaseRequest
	if json.Unmarshal(document, &input) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	request := nativeworker.ReleaseRequest{
		LeaseID: leaseID, AttemptID: input.AttemptID,
		ExpectedRunRevision: input.ExpectedRunRevision,
		Reason:              input.Reason, Message: input.Message,
		Capability: capabilities[0],
	}
	if !request.Valid() {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	err := handler.release(r.Context(), identity.WorkerID, request)
	switch {
	case errors.Is(err, nativeworker.ErrLeaseGone):
		fail(w, http.StatusGone, "LEASE_EXPIRED")
	case errors.Is(err, run.ErrRevision), errors.Is(err, run.ErrConflict),
		errors.Is(err, run.ErrTransition):
		fail(w, http.StatusConflict, "REVISION_CONFLICT")
	case errors.Is(err, run.ErrValidation):
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, run.ErrUnavailable), errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
	case err != nil:
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
	default:
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNoContent)
	}
}

func nativeWorkerReleaseLeaseID(path string) (string, bool) {
	const prefix = nativeWorkerLeasePath + "/"
	const suffix = "/release"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	leaseID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)

	return leaseID, leaseID != "" && !strings.Contains(leaseID, "/")
}
