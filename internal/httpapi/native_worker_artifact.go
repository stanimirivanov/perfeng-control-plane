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

type nativeArtifactRegistrationRequest struct {
	AttemptID           string `json:"attemptId"`
	ExpectedRunRevision int64  `json:"expectedRunRevision"`
	GrantID             string `json:"grantId"`
	Slot                string `json:"slot"`
	Size                int64  `json:"size"`
	SHA256              string `json:"sha256"`
}

func (handler *NativeWorkerHandler) registerArtifact(
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
	if contract.ValidateRegisterArtifact(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input nativeArtifactRegistrationRequest
	if json.Unmarshal(document, &input) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	request := nativeworker.ArtifactRegistrationRequest{
		LeaseID: leaseID, AttemptID: input.AttemptID,
		ExpectedRunRevision: input.ExpectedRunRevision,
		GrantID:             input.GrantID, Slot: input.Slot, Size: input.Size, SHA256: input.SHA256,
		Capability: capabilities[0],
	}
	if !request.Valid() {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	reference, err := handler.register(r.Context(), identity.WorkerID, request)
	switch {
	case errors.Is(err, nativeworker.ErrLeaseGone):
		fail(w, http.StatusGone, "LEASE_EXPIRED")
		return
	case errors.Is(err, nativeworker.ErrUploadGrantMismatch),
		errors.Is(err, nativeworker.ErrArtifactPlan),
		errors.Is(err, nativeworker.ErrArtifactVerification):
		fail(w, http.StatusConflict, "ARTIFACT_MISMATCH")
		return
	case errors.Is(err, run.ErrRevision):
		fail(w, http.StatusConflict, "REVISION_CONFLICT")
		return
	case errors.Is(err, run.ErrConflict):
		fail(w, http.StatusConflict, "DUPLICATE_CONFLICT")
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
	case !reference.Valid():
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}

	writeJSON(w, http.StatusCreated, reference)
}

func nativeWorkerArtifactLeaseID(path string) (string, bool) {
	const prefix = nativeWorkerLeasePath + "/"
	const suffix = "/artifacts"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	leaseID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)

	return leaseID, leaseID != "" && !strings.Contains(leaseID, "/")
}
