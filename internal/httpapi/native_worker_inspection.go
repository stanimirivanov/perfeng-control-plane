package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type nativeInspectAttemptRequest struct {
	RunID     string `json:"runId"`
	AttemptID string `json:"attemptId"`
}

func (handler *NativeWorkerHandler) inspectAttempt(
	w http.ResponseWriter,
	r *http.Request,
	identity WorkerIdentity,
) {
	if !identity.AuthorizedToAcquire && !identity.AuthorizedToUseLease {
		fail(w, http.StatusForbidden, "NOT_AUTHORIZED")
		return
	}
	document, value, ok := readJSONBodyWithCode(w, r, "INVALID_REQUEST")
	if !ok {
		return
	}
	if contract.ValidateNativeWorkerV2InspectAttempt(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	var input nativeInspectAttemptRequest
	if json.Unmarshal(document, &input) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	request := nativeworker.InspectionRequest{RunID: input.RunID, AttemptID: input.AttemptID}
	if !request.Valid() {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	inspection, err := handler.store.InspectNativeBaselineAttempt(
		r.Context(), identity.WorkerID, request,
	)
	switch {
	case errors.Is(err, nativeworker.ErrAttemptNotFound):
		fail(w, http.StatusNotFound, "ATTEMPT_NOT_FOUND")
	case errors.Is(err, run.ErrValidation):
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, run.ErrUnavailable), errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
	case err != nil || !inspection.Valid() || inspection.RunID != request.RunID ||
		inspection.AttemptID != request.AttemptID:
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
	default:
		writeJSON(w, http.StatusOK, inspection)
	}
}
