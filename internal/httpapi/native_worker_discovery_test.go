package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type discoveryStoreStub struct {
	nativePresenceStoreStub
	result   nativeworker.Discovery
	err      error
	workerID string
	calls    int
}

func (store *discoveryStoreStub) DiscoverNativeBaselineAttempt(_ context.Context, workerID string) (nativeworker.Discovery, error) {
	store.calls++
	store.workerID = workerID
	return store.result, store.err
}

func discoveryHandler(t *testing.T, store *discoveryStoreStub, authorized bool) *NativeWorkerHandler {
	t.Helper()
	_, evidence := nativePresenceFixture(t)
	handler, err := NewNativeWorkerHandler(store,
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{WorkerID: "windows-laptop-01", AuthorizedToUseLease: authorized,
				QualificationEvidence: evidence}, nil
		}, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveDiscovery(handler *NativeWorkerHandler, method, path, body string, token bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token {
		request.Header.Set("Authorization", "Bearer worker-token")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestNativeWorkerDiscoveryReturnsOnlyPrincipalScopedSnapshots(t *testing.T) {
	for _, name := range []string{"discovery-active.json", "discovery-terminal.json",
		"discovery-none.json", "discovery-available.json", "discovery-unreported.json"} {
		t.Run(name, func(t *testing.T) {
			var expected nativeworker.Discovery
			if err := json.Unmarshal(inspectionFixture(t, name), &expected); err != nil {
				t.Fatal(err)
			}
			store := &discoveryStoreStub{result: expected}
			response := serveDiscovery(discoveryHandler(t, store, true), http.MethodPost,
				nativeWorkerDiscoverPath, `{"protocolVersion":2}`, true)
			if response.Code != http.StatusOK || store.calls != 1 || store.workerID != "windows-laptop-01" {
				t.Fatalf("status=%d calls=%d", response.Code, store.calls)
			}
			decoder := json.NewDecoder(response.Body)
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil || contract.ValidateNativeWorkerV2AttemptDiscovery(value) != nil {
				t.Fatalf("invalid discovery response: %v", err)
			}
			if response.Header().Get("X-Perfeng-Lease") != "" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("discovery exposed authority or allowed caching")
			}
		})
	}
}

func TestNativeWorkerDiscoveryRejectsUntrustedInputsBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		token, authorized        bool
		status                   int
	}{
		{"missing credential", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2}`, false, true, 401},
		{"no permission", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2}`, true, false, 403},
		{"worker selector", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2,"workerId":"other"}`, true, true, 400},
		{"run selector", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2,"runId":"other"}`, true, true, 400},
		{"query selector", "POST", nativeWorkerDiscoverPath + "?workerId=other", `{"protocolVersion":2}`, true, true, 400},
		{"retired protocol", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":1}`, true, true, 400},
		{"missing protocol", "POST", nativeWorkerDiscoverPath, `{}`, true, true, 400},
		{"trailing document", "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2}{}`, true, true, 400},
		{"oversized document", "POST", nativeWorkerDiscoverPath, strings.Repeat(" ", MaxBodyBytes+1), true, true, 413},
		{"wrong method", "GET", nativeWorkerDiscoverPath, `{"protocolVersion":2}`, true, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &discoveryStoreStub{}
			response := serveDiscovery(discoveryHandler(t, store, tc.authorized), tc.method, tc.path, tc.body, tc.token)
			if response.Code != tc.status || store.calls != 0 {
				t.Fatalf("status=%d calls=%d", response.Code, store.calls)
			}
		})
	}
}

func TestNativeWorkerDiscoveryFailsClosedWithoutBackendDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{run.ErrUnavailable, 503}, {context.Canceled, 503}, {context.DeadlineExceeded, 503},
		{errors.New("private capability or database detail"), 500}, {nil, 500},
	} {
		store := &discoveryStoreStub{err: tc.err}
		response := serveDiscovery(discoveryHandler(t, store, true), "POST", nativeWorkerDiscoverPath, `{"protocolVersion":2}`, true)
		if response.Code != tc.status || store.calls != 1 || bytes.Contains(response.Body.Bytes(), []byte("private")) {
			t.Fatalf("status=%d calls=%d", response.Code, store.calls)
		}
	}
}
