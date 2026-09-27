package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeWorkerUploadGrantBindsAuthenticatedAuthority(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	want := nativeUploadGrantFixture(t)
	var calledWorker string
	var calledRequest nativeworker.UploadRequest
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
		noNativeRelease,
		func(
			_ context.Context,
			workerID string,
			request nativeworker.UploadRequest,
		) (nativeworker.UploadGrant, error) {
			calledWorker = workerID
			calledRequest = request

			return want, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeUploadGrant(t, handler, nativeUploadRequestFixture(t), nativeLeaseCapability)
	if response.Code != http.StatusCreated || calledWorker != "windows-laptop-01" ||
		calledRequest.LeaseID != want.LeaseID || calledRequest.AttemptID != want.AttemptID ||
		calledRequest.ExpectedRunRevision != 5 || calledRequest.Slot != want.Slot ||
		calledRequest.Size != 4096 || calledRequest.SHA256 != want.SHA256 ||
		calledRequest.MediaType != want.UploadHeaders.ContentType ||
		calledRequest.Capability != nativeLeaseCapability {
		t.Fatalf(
			"status = %d, worker = %q, request = %#v",
			response.Code,
			calledWorker,
			calledRequest,
		)
	}
	var actual nativeworker.UploadGrant
	if json.Unmarshal(response.Body.Bytes(), &actual) != nil || !actual.Valid() ||
		actual.GrantID != want.GrantID || actual.UploadURL != want.UploadURL ||
		actual.UploadHeaders != want.UploadHeaders {
		t.Fatalf("upload grant = %#v", actual)
	}
	if strings.Contains(response.Body.String(), nativeLeaseCapability) ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v, body = %q", response.Header(), response.Body)
	}
}

func TestNativeWorkerUploadGrantRejectsMissingOrInvalidAuthority(t *testing.T) {
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
		noNativeRelease,
		func(context.Context, string, nativeworker.UploadRequest) (nativeworker.UploadGrant, error) {
			called = true

			return nativeworker.UploadGrant{}, nil
		},
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
			response := serveNativeUploadGrant(t, handler, nativeUploadRequestFixture(t), capability)
			assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
			if called {
				t.Fatal("upload grant called without valid authority")
			}
		})
	}

	request := newNativeUploadGrantRequest(t, nativeUploadRequestFixture(t), nativeLeaseCapability)
	request.Header.Add("X-Perfeng-Lease", nativeLeaseCapability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
	if called {
		t.Fatal("upload grant called with duplicate capability headers")
	}
}

func TestNativeWorkerUploadGrantRequiresCurrentWorkerAuthorization(t *testing.T) {
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
		noNativeRelease,
		func(context.Context, string, nativeworker.UploadRequest) (nativeworker.UploadGrant, error) {
			called = true

			return nativeworker.UploadGrant{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeUploadGrant(t, handler, nativeUploadRequestFixture(t), nativeLeaseCapability)
	assertNativeWorkerError(t, response, http.StatusForbidden, "NOT_AUTHORIZED")
	if called {
		t.Fatal("upload grant called for unauthorized worker")
	}
}

func TestNativeWorkerUploadGrantRejectsInvalidRequestBeforeReservation(t *testing.T) {
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
		noNativeRelease,
		func(context.Context, string, nativeworker.UploadRequest) (nativeworker.UploadGrant, error) {
			called = true

			return nativeworker.UploadGrant{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		leaseID string
		body    string
	}{
		"lease id": {"not-a-lease", string(nativeUploadRequestFixture(t))},
		"attempt": {nativeLeaseFixture(t).LeaseID,
			`{"attemptId":"bad","expectedRunRevision":5,"slot":"measurements","size":4096,"sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","mediaType":"application/json"}`},
		"unknown": {nativeLeaseFixture(t).LeaseID,
			`{"attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":5,"slot":"measurements","size":4096,"sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","mediaType":"application/json","objectKey":"worker-selected"}`},
	} {
		t.Run(name, func(t *testing.T) {
			request := newNativeUploadGrantRequest(
				t, []byte(test.body), nativeLeaseCapability,
			)
			request.URL.Path = nativeWorkerLeasePath + "/" + test.leaseID + "/upload-grants"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertNativeWorkerError(t, response, http.StatusBadRequest, "INVALID_REQUEST")
			if called {
				t.Fatal("upload grant called for invalid request")
			}
		})
	}
}

func TestNativeWorkerUploadGrantMapsAuthorityAndDependencyFailures(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	unencodable := nativeUploadGrantFixture(t)
	unencodable.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	for name, test := range map[string]struct {
		err    error
		grant  nativeworker.UploadGrant
		status int
		code   string
	}{
		"gone":              {err: nativeworker.ErrLeaseGone, status: http.StatusGone, code: "LEASE_EXPIRED"},
		"artifact mismatch": {err: nativeworker.ErrArtifactPlan, status: http.StatusConflict, code: "ARTIFACT_MISMATCH"},
		"revision":          {err: run.ErrRevision, status: http.StatusConflict, code: "REVISION_CONFLICT"},
		"duplicate":         {err: run.ErrConflict, status: http.StatusConflict, code: "DUPLICATE_CONFLICT"},
		"validation":        {err: run.ErrValidation, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		"unavailable":       {err: run.ErrUnavailable, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"canceled":          {err: context.Canceled, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"deadline":          {err: context.DeadlineExceeded, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"internal":          {err: errors.New("storage detail"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		"invalid grant":     {grant: nativeworker.UploadGrant{}, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		"unencodable grant": {grant: unencodable, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, noNativeLease, noNativeRenewal,
				noNativeRelease,
				func(context.Context, string, nativeworker.UploadRequest) (nativeworker.UploadGrant, error) {
					return test.grant, test.err
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeUploadGrant(
				t, handler, nativeUploadRequestFixture(t), nativeLeaseCapability,
			)
			assertNativeWorkerError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "storage detail") ||
				(test.grant.UploadURL != "" && strings.Contains(response.Body.String(), test.grant.UploadURL)) {
				t.Fatal("upload failure exposed dependency or delivery detail")
			}
		})
	}
}

func TestNativeWorkerUploadGrantRouteIsExact(t *testing.T) {
	const leaseID = "11111111-1111-4111-8111-111111111111"
	for path, want := range map[string]bool{
		nativeWorkerLeasePath + "/" + leaseID + "/upload-grants":           true,
		nativeWorkerLeasePath + "/not-a-uuid/upload-grants":                true,
		nativeWorkerLeasePath + "/" + leaseID + "/release":                 false,
		nativeWorkerLeasePath + "/" + leaseID + "/upload-grants/duplicate": false,
		nativeWorkerLeasePath + "//upload-grants":                          false,
	} {
		actualID, actual := nativeWorkerUploadGrantLeaseID(path)
		if actual != want || (actual && actualID == "") {
			t.Fatalf("path %q: id = %q, matched = %t", path, actualID, actual)
		}
	}
}

func serveNativeUploadGrant(
	t *testing.T,
	handler http.Handler,
	document []byte,
	capability string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newNativeUploadGrantRequest(t, document, capability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func newNativeUploadGrantRequest(
	t *testing.T,
	document []byte,
	capability string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		nativeWorkerLeasePath+"/"+nativeLeaseFixture(t).LeaseID+"/upload-grants",
		strings.NewReader(string(document)),
	)
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("X-Perfeng-Lease", capability)
	}

	return request
}

func nativeUploadRequestFixture(t *testing.T) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/upload-request.json")
	if err != nil {
		t.Fatal(err)
	}

	return document
}

func nativeUploadGrantFixture(t *testing.T) nativeworker.UploadGrant {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/upload-grant.json")
	if err != nil {
		t.Fatal(err)
	}
	var grant nativeworker.UploadGrant
	if json.Unmarshal(document, &grant) != nil || !grant.Valid() {
		t.Fatal("invalid pinned upload grant fixture")
	}

	return grant
}
