package httpapi

import (
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

func TestNativeWorkerReleaseBindsAuthenticatedAuthority(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	var calledWorker string
	var calledRequest nativeworker.ReleaseRequest
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		func(_ context.Context, workerID string, request nativeworker.ReleaseRequest) error {
			calledWorker = workerID
			calledRequest = request

			return nil
		},
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeRelease(t, handler, nativeReleaseFixture(t), nativeLeaseCapability)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 ||
		calledWorker != "windows-laptop-01" ||
		calledRequest.LeaseID != nativeLeaseFixture(t).LeaseID ||
		calledRequest.AttemptID != nativeLeaseFixture(t).AttemptID ||
		calledRequest.ExpectedRunRevision != 5 ||
		calledRequest.Reason != nativeworker.ReleaseCancellationRequested ||
		calledRequest.Message != "" || calledRequest.Capability != nativeLeaseCapability {
		t.Fatalf(
			"status = %d, body = %q, worker = %q, request = %#v",
			response.Code,
			response.Body,
			calledWorker,
			calledRequest,
		)
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", response.Header())
	}
}

func TestNativeWorkerReleaseRejectsMissingOrInvalidAuthorityBeforeRelease(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		func(context.Context, string, nativeworker.ReleaseRequest) error {
			called = true

			return nil
		},
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	for name, capability := range map[string]string{
		"missing": "",
		"short":   "not-authority",
		"unsafe":  strings.Repeat("x", 31) + "\t",
	} {
		t.Run(name, func(t *testing.T) {
			response := serveNativeRelease(t, handler, nativeReleaseFixture(t), capability)
			assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
			if called {
				t.Fatal("release called without valid authority")
			}
		})
	}

	request := newNativeReleaseRequest(t, nativeReleaseFixture(t), nativeLeaseCapability)
	request.Header.Add("X-Perfeng-Lease", nativeLeaseCapability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
	if called {
		t.Fatal("release called with duplicate capability headers")
	}
}

func TestNativeWorkerReleaseRequiresCurrentWorkerAuthorization(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		func(context.Context, string, nativeworker.ReleaseRequest) error {
			called = true

			return nil
		},
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeRelease(t, handler, nativeReleaseFixture(t), nativeLeaseCapability)
	assertNativeWorkerError(t, response, http.StatusForbidden, "NOT_AUTHORIZED")
	if called {
		t.Fatal("release called for unauthorized worker")
	}
}

func TestNativeWorkerReleaseRejectsInvalidRequestBeforeRelease(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		func(context.Context, string, nativeworker.ReleaseRequest) error {
			called = true

			return nil
		},
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		leaseID string
		body    string
	}{
		"lease id": {"not-a-lease", string(nativeReleaseFixture(t))},
		"attempt":  {nativeLeaseFixture(t).LeaseID, `{"attemptId":"bad","expectedRunRevision":5,"reason":"CANCELLATION_REQUESTED"}`},
		"reason":   {nativeLeaseFixture(t).LeaseID, `{"attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":5,"reason":"UNKNOWN"}`},
		"message":  {nativeLeaseFixture(t).LeaseID, `{"attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":5,"reason":"LEASE_LOST","message":""}`},
		"unknown":  {nativeLeaseFixture(t).LeaseID, `{"attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":5,"reason":"LEASE_LOST","workerId":"chosen"}`},
	} {
		t.Run(name, func(t *testing.T) {
			request := newNativeReleaseRequest(t, []byte(test.body), nativeLeaseCapability)
			request.URL.Path = nativeWorkerLeasePath + "/" + test.leaseID + "/release"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertNativeWorkerError(t, response, http.StatusBadRequest, "INVALID_REQUEST")
			if called {
				t.Fatal("release called for invalid request")
			}
		})
	}
}

func TestNativeWorkerReleaseMapsAuthorityAndConcurrencyFailures(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	identity := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"gone":        {nativeworker.ErrLeaseGone, http.StatusGone, "LEASE_EXPIRED"},
		"revision":    {run.ErrRevision, http.StatusConflict, "REVISION_CONFLICT"},
		"conflict":    {run.ErrConflict, http.StatusConflict, "REVISION_CONFLICT"},
		"validation":  {run.ErrValidation, http.StatusBadRequest, "INVALID_REQUEST"},
		"unavailable": {run.ErrUnavailable, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
		"internal":    {errors.New("storage failure"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, noNativeLease, noNativeRenewal,
				func(context.Context, string, nativeworker.ReleaseRequest) error {
					return test.err
				},
				noNativeUploadGrant,
				noNativeArtifactRegistration,
			)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeRelease(t, handler, nativeReleaseFixture(t), nativeLeaseCapability)
			assertNativeWorkerError(t, response, test.status, test.code)
		})
	}
}

func TestNativeWorkerReleaseRouteIsExact(t *testing.T) {
	const leaseID = "11111111-1111-4111-8111-111111111111"
	for path, want := range map[string]bool{
		nativeWorkerLeasePath + "/" + leaseID + "/release":           true,
		nativeWorkerLeasePath + "/not-a-uuid/release":                true,
		nativeWorkerLeasePath + "/" + leaseID + "/renew":             false,
		nativeWorkerLeasePath + "/" + leaseID + "/release/duplicate": false,
		nativeWorkerLeasePath + "//release":                          false,
	} {
		actualID, actual := nativeWorkerReleaseLeaseID(path)
		if actual != want || (actual && actualID == "") {
			t.Fatalf("path %q: id = %q, matched = %t", path, actualID, actual)
		}
	}
}

func serveNativeRelease(
	t *testing.T,
	handler http.Handler,
	document []byte,
	capability string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newNativeReleaseRequest(t, document, capability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func newNativeReleaseRequest(
	t *testing.T,
	document []byte,
	capability string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		nativeWorkerLeasePath+"/"+nativeLeaseFixture(t).LeaseID+"/release",
		strings.NewReader(string(document)),
	)
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("X-Perfeng-Lease", capability)
	}

	return request
}

func nativeReleaseFixture(t *testing.T) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/release.json")
	if err != nil {
		t.Fatal(err)
	}

	return document
}

func assertNativeWorkerError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	var body apiError
	if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		response.Code != wantStatus || body.Code != wantCode {
		t.Fatalf("status = %d, error = %#v", response.Code, body)
	}
}
