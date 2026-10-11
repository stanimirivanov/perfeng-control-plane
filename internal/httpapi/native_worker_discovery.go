package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func (handler *NativeWorkerHandler) discoverAttempt(w http.ResponseWriter, r *http.Request, identity WorkerIdentity) {
	if !identity.AuthorizedToAcquire && !identity.AuthorizedToUseLease {
		fail(w, http.StatusForbidden, "NOT_AUTHORIZED")
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	_, value, ok := readJSONBodyWithCode(w, r, "INVALID_REQUEST")
	if !ok {
		return
	}
	if contract.ValidateNativeWorkerV2DiscoverAttempt(value) != nil {
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	discovery, err := handler.store.DiscoverNativeBaselineAttempt(r.Context(), identity.WorkerID)
	switch {
	case errors.Is(err, run.ErrValidation):
		fail(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, run.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE")
	case err != nil || !discovery.Valid():
		fail(w, http.StatusInternalServerError, "INTERNAL_ERROR")
	default:
		writeJSON(w, http.StatusOK, discovery)
	}
}
