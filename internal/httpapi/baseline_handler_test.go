package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/memory"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func baselineFixture(t *testing.T, runID string) ([]byte, run.Artifact) {
	t.Helper()
	b, err := contract.Files.ReadFile("snapshot/examples/baseline-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(b, &request); err != nil {
		t.Fatal(err)
	}
	request["sourceRunId"] = runID
	artifact := request["artifact"].(map[string]any)
	artifact["runId"] = runID
	artifact["uri"] = strings.Replace(artifact["uri"].(string),
		"perf-20260904-130000-a1b2c3d6", runID, 1)
	b, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var reference run.Artifact
	if err := json.Unmarshal(mustMarshal(t, artifact), &reference); err != nil {
		t.Fatal(err)
	}
	value, err := parseJSON(b)
	if err != nil || contract.ValidateBaselineCreate(value) != nil {
		t.Fatal("generated baseline request does not match the contract")
	}

	return b, reference
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func completedEvidence(
	t *testing.T,
	h *Handler,
	repository *memory.Repository,
) ([]byte, string) {
	t.Helper()
	response := call(h, http.MethodPost, "/v1/runs", "alice-token", fixture(t))
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body)
	}
	current := decodeRun(t, response)
	for _, state := range []run.State{
		run.StateValidating,
		run.StateProvisioning,
		run.StateRunning,
		run.StateCollecting,
		run.StateAnalyzing,
		run.StateReporting,
		run.StateCompleted,
	} {
		var err error
		current, err = repository.Advance(
			context.Background(), "alice", current.ID, current.Revision, run.Change{State: state},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	body, artifact := baselineFixture(t, current.ID)
	if err := repository.RegisterArtifact(context.Background(), "alice", artifact); err != nil {
		t.Fatal(err)
	}

	return body, "/v1/baselines/approved-search-browser/versions/2.0.0"
}

func decodeBaseline(t *testing.T, responseBody []byte) baseline.Record {
	t.Helper()
	var record baseline.Record
	if err := json.Unmarshal(responseBody, &record); err != nil {
		t.Fatal(err)
	}

	return record
}

func TestBaselineAdministrationLifecycle(t *testing.T) {
	h, repository := setup(t)
	body, location := completedEvidence(t, h, repository)

	created := call(h, http.MethodPost, "/v1/baselines", "alice-token", body)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != location {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	candidate := decodeBaseline(t, created.Body.Bytes())
	if candidate.State != baseline.StateCandidate || candidate.Revision != 1 ||
		candidate.Lifecycle[0].Actor != "alice" {
		t.Fatalf("candidate = %#v", candidate)
	}
	read := call(h, http.MethodGet, location, "read-only", nil)
	if read.Code != http.StatusOK || read.Body.String() != created.Body.String() {
		t.Fatalf("read = %d %s", read.Code, read.Body)
	}
	checkError(t, call(h, http.MethodGet, location, "bob-token", nil), 404, "NOT_FOUND")
	checkError(t, call(h, http.MethodPost, "/v1/baselines", "alice-token", body),
		409, "BASELINE_EXISTS")

	directApproval := []byte(`{"expectedRevision":1,"state":"APPROVED","reason":"skip review"}`)
	checkError(t, call(h, http.MethodPost, location+"/transitions", "alice-token", directApproval),
		409, "BASELINE_TRANSITION_CONFLICT")

	qualification := []byte(`{
		"expectedRevision": 1,
		"state": "QUALIFIED",
		"qualification": {
			"status": "PASSED",
			"reasons": [],
			"sampleCount": 30,
			"maximumCv": 0.05
		},
		"reason": "Evidence passed review."
	}`)
	qualifiedResponse := call(
		h, http.MethodPost, location+"/transitions", "alice-token", qualification,
	)
	qualified := decodeBaseline(t, qualifiedResponse.Body.Bytes())
	if qualifiedResponse.Code != http.StatusOK || qualified.State != baseline.StateQualified ||
		qualified.Revision != 2 || qualified.Lifecycle[1].Actor != "alice" {
		t.Fatalf("qualified = %d %#v", qualifiedResponse.Code, qualified)
	}

	checkError(t, call(h, http.MethodPost, location+"/transitions", "alice-token", qualification),
		409, "REVISION_CONFLICT")
	approvedResponse := call(
		h, http.MethodPost, location+"/transitions", "alice-token",
		[]byte(`{"expectedRevision":2,"state":"APPROVED","reason":"Approved anchor."}`),
	)
	approved := decodeBaseline(t, approvedResponse.Body.Bytes())
	if approvedResponse.Code != http.StatusOK || approved.State != baseline.StateApproved ||
		approved.Revision != 3 || approved.Lifecycle[2].Actor != "alice" {
		t.Fatalf("approved = %d %#v", approvedResponse.Code, approved)
	}
}

func TestBaselineRequestBoundariesAndPermissions(t *testing.T) {
	h, repository := setup(t)
	body, location := completedEvidence(t, h, repository)

	checkError(t, call(h, http.MethodPost, "/v1/baselines", "read-only", body),
		403, "FORBIDDEN")
	checkError(t, call(h, http.MethodPost, "/v1/baselines", "create-only", body),
		403, "FORBIDDEN")
	withActor := strings.Replace(string(body), "{", `{"actor":"mallory",`, 1)
	checkError(t, call(h, http.MethodPost, "/v1/baselines", "alice-token", []byte(withActor)),
		422, "VALIDATION_FAILED")
	checkError(t, call(h, http.MethodPost, "/v1/baselines", "alice-token", []byte("{")),
		400, "BAD_REQUEST")
	checkError(t, call(h, http.MethodPost, "/v1/baselines", "alice-token",
		append(body, make([]byte, MaxBodyBytes)...)), 413, "BAD_REQUEST")
	checkError(t, call(h, http.MethodGet, "/v1/baselines/Bad/versions/2.0.0", "alice-token", nil),
		400, "BAD_REQUEST")

	created := call(h, http.MethodPost, "/v1/baselines", "alice-token", body)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body)
	}
	unknown := []byte(`{"expectedRevision":1,"state":"RETIRED","reason":"No longer used.","actor":"mallory"}`)
	checkError(t, call(h, http.MethodPost, location+"/transitions", "alice-token", unknown),
		422, "VALIDATION_FAILED")
	checkError(t, call(h, http.MethodPost, location+"/transitions", "read-only",
		[]byte(`{"expectedRevision":1,"state":"RETIRED","reason":"No longer used."}`)),
		403, "FORBIDDEN")
}
