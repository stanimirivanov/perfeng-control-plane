package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type inspectionStoreStub struct {
	nativePresenceStoreStub
	workerID string
	request  nativeworker.InspectionRequest
	result   nativeworker.Inspection
	err      error
	calls    int
}

func (store *inspectionStoreStub) InspectNativeBaselineAttempt(
	_ context.Context, workerID string, request nativeworker.InspectionRequest,
) (nativeworker.Inspection, error) {
	store.calls++
	store.workerID = workerID
	store.request = request
	return store.result, store.err
}

func inspectionHandler(t *testing.T, store *inspectionStoreStub, authorized bool) *NativeWorkerHandler {
	t.Helper()
	_, evidence := nativePresenceFixture(t)
	handler, err := NewNativeWorkerHandler(
		store,
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToUseLease: authorized,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal,
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveInspection(handler *NativeWorkerHandler, method string, body []byte, token bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, nativeWorkerInspectPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token {
		request.Header.Set("Authorization", "Bearer worker-token")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func inspectionFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNativeWorkerInspectionBindsPrincipalAndReturnsClosedSnapshots(t *testing.T) {
	request := inspectionFixture(t, "inspect-attempt.json")
	for _, name := range []string{"attempt-active.json", "attempt-inactive.json", "attempt-terminal.json"} {
		t.Run(name, func(t *testing.T) {
			var expected nativeworker.Inspection
			if err := json.Unmarshal(inspectionFixture(t, name), &expected); err != nil {
				t.Fatal(err)
			}
			store := &inspectionStoreStub{result: expected}
			response := serveInspection(inspectionHandler(t, store, true), http.MethodPost, request, true)
			if response.Code != http.StatusOK || store.calls != 1 ||
				store.workerID != "windows-laptop-01" ||
				store.request.RunID != expected.RunID || store.request.AttemptID != expected.AttemptID {
				t.Fatalf("status=%d, store=%#v", response.Code, store)
			}
			var value any
			decoder := json.NewDecoder(response.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil ||
				contract.ValidateNativeWorkerV2AttemptInspection(value) != nil {
				t.Fatalf("invalid inspection response: %v", err)
			}
			if response.Header().Get("X-Perfeng-Lease") != "" ||
				response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("inspection exposed authority or allowed caching")
			}
		})
	}
}

func TestNativeWorkerInspectionRejectsUntrustedInputsBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		body       []byte
		token      bool
		authorized bool
		status     int
	}{
		{"missing credential", http.MethodPost, inspectionFixture(t, "inspect-attempt.json"), false, true, http.StatusUnauthorized},
		{"no permission", http.MethodPost, inspectionFixture(t, "inspect-attempt.json"), true, false, http.StatusForbidden},
		{"selected worker", http.MethodPost, []byte(`{"protocolVersion":2,"runId":"perf-20260918-120000-a1b2c3d4","attemptId":"22222222-2222-4222-8222-222222222222","workerId":"other"}`), true, true, http.StatusBadRequest},
		{"wrong protocol", http.MethodPost, []byte(`{"protocolVersion":1,"runId":"perf-20260918-120000-a1b2c3d4","attemptId":"22222222-2222-4222-8222-222222222222"}`), true, true, http.StatusBadRequest},
		{"wrong method", http.MethodGet, inspectionFixture(t, "inspect-attempt.json"), true, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &inspectionStoreStub{}
			response := serveInspection(inspectionHandler(t, store, tc.authorized), tc.method, tc.body, tc.token)
			if response.Code != tc.status || store.calls != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, store.calls)
			}
		})
	}
}

func TestNativeWorkerInspectionConcealsAbsenceAndBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
		code string
	}{
		{nativeworker.ErrAttemptNotFound, http.StatusNotFound, "ATTEMPT_NOT_FOUND"},
		{run.ErrUnavailable, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
		{errors.New("private storage detail"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		store := &inspectionStoreStub{err: tc.err}
		response := serveInspection(inspectionHandler(t, store, true),
			http.MethodPost, inspectionFixture(t, "inspect-attempt.json"), true)
		var body apiError
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil ||
			response.Code != tc.want || body.Code != tc.code || store.calls != 1 ||
			bytes.Contains(response.Body.Bytes(), []byte("private storage detail")) {
			t.Fatalf("status=%d, body=%s, err=%v", response.Code, response.Body.String(), err)
		}
	}
}
