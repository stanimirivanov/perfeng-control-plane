package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type baselineCreateRequest struct {
	ID          string               `json:"id"`
	Version     string               `json:"version"`
	TestID      string               `json:"testId"`
	SourceRunID string               `json:"sourceRunId"`
	Artifact    run.Artifact         `json:"artifact"`
	Software    baseline.Software    `json:"software"`
	Workload    rawresult.Identity   `json:"workload"`
	Environment baseline.Environment `json:"environment"`
	Dataset     baseline.Dataset     `json:"dataset"`
	Reason      string               `json:"reason"`
}

func (request baselineCreateRequest) create(actor string) baseline.Create {
	return baseline.Create{
		ID: request.ID, Version: request.Version, TestID: request.TestID,
		SourceRunID: request.SourceRunID, Artifact: request.Artifact,
		Software: request.Software, Workload: request.Workload,
		Environment: request.Environment, Dataset: request.Dataset,
		Actor: actor, Reason: request.Reason,
	}
}

type baselineTransitionRequest struct {
	ExpectedRevision int64                   `json:"expectedRevision"`
	State            baseline.State          `json:"state"`
	Qualification    *baseline.Qualification `json:"qualification,omitempty"`
	Reason           string                  `json:"reason"`
}

func (h *Handler) createBaseline(w http.ResponseWriter, r *http.Request, principal string) {
	b, value, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if contract.ValidateBaselineCreate(value) != nil {
		fail(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		return
	}
	var request baselineCreateRequest
	if json.Unmarshal(b, &request) != nil {
		fail(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		return
	}
	record, err := h.repository.CreateBaseline(r.Context(), principal, request.create(principal))
	if err != nil {
		failBaselineCreate(w, err)
		return
	}
	w.Header().Set("Location", baselineLocation(record.ID, record.Version))
	writeJSON(w, http.StatusCreated, record)
}

func (h *Handler) serveBaseline(
	w http.ResponseWriter,
	r *http.Request,
	identity Identity,
	path string,
) {
	parts := strings.Split(strings.TrimPrefix(path, "/v1/baselines/"), "/")
	isGet := len(parts) == 3 && parts[1] == "versions" && r.Method == http.MethodGet
	isTransition := len(parts) == 4 && parts[1] == "versions" &&
		parts[3] == "transitions" && r.Method == http.MethodPost
	if !isGet && !isTransition {
		fail(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if (isGet && !identity.ReadBaseline) || (isTransition && !identity.TransitionBaseline) {
		fail(w, http.StatusForbidden, "FORBIDDEN")
		return
	}
	if !contract.ValidResourceID(parts[0]) || !contract.ValidVersion(parts[2]) {
		fail(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if isTransition {
		h.transitionBaseline(w, r, identity.Principal, parts[0], parts[2])
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) > 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	record, err := h.repository.GetBaseline(r.Context(), identity.Principal, parts[0], parts[2])
	if err != nil {
		failRepository(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) transitionBaseline(
	w http.ResponseWriter,
	r *http.Request,
	principal, id, version string,
) {
	b, value, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	if contract.ValidateBaselineTransition(value) != nil {
		fail(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		return
	}
	var request baselineTransitionRequest
	if json.Unmarshal(b, &request) != nil {
		fail(w, http.StatusUnprocessableEntity, "VALIDATION_FAILED")
		return
	}
	record, err := h.repository.TransitionBaseline(
		r.Context(), principal, id, version, request.ExpectedRevision,
		baseline.Change{
			State: request.State, Qualification: request.Qualification,
			Actor: principal, Reason: request.Reason,
		},
	)
	if err != nil {
		failBaselineTransition(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func baselineLocation(id, version string) string {
	return "/v1/baselines/" + id + "/versions/" + version
}

func failBaselineCreate(w http.ResponseWriter, err error) {
	if errors.Is(err, run.ErrConflict) {
		fail(w, http.StatusConflict, "BASELINE_EXISTS")
		return
	}
	failRepository(w, err)
}

func failBaselineTransition(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, run.ErrRevision):
		fail(w, http.StatusConflict, "REVISION_CONFLICT")
	case errors.Is(err, baseline.ErrTransition):
		fail(w, http.StatusConflict, "BASELINE_TRANSITION_CONFLICT")
	default:
		failRepository(w, err)
	}
}
