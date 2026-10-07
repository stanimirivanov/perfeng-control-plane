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

func TestNativeWorkerRenewBindsAuthenticatedAuthorityAndReturnsCancellation(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	want := nativeRenewalFixture(t)
	var calledWorker string
	var calledRequest nativeworker.RenewalRequest
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		func(
			_ context.Context,
			workerID string,
			request nativeworker.RenewalRequest,
		) (nativeworker.Renewal, error) {
			calledWorker = workerID
			calledRequest = request
			return want, nil
		},
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeRenew(t, handler, nativeRenewFixture(t), nativeLeaseCapability)
	if response.Code != http.StatusOK || calledWorker != "windows-laptop-01" ||
		calledRequest.LeaseID != want.LeaseID || calledRequest.AttemptID != want.AttemptID ||
		calledRequest.ExpectedRunRevision != 4 || calledRequest.Phase != nativeworker.PhaseRunning ||
		calledRequest.Capability != nativeLeaseCapability {
		t.Fatalf(
			"status = %d, worker = %q, request = %#v",
			response.Code,
			calledWorker,
			calledRequest,
		)
	}
	var actual nativeworker.Renewal
	if json.Unmarshal(response.Body.Bytes(), &actual) != nil ||
		actual.LeaseID != want.LeaseID || actual.AttemptID != want.AttemptID ||
		actual.RunRevision != want.RunRevision || !actual.ServerTime.Equal(want.ServerTime) ||
		!actual.ExpiresAt.Equal(want.ExpiresAt) ||
		actual.RenewAfterSeconds != want.RenewAfterSeconds ||
		actual.CancellationRequested != want.CancellationRequested || actual.StopBy == nil ||
		want.StopBy == nil || !actual.StopBy.Equal(*want.StopBy) {
		t.Fatalf("renewal = %#v", actual)
	}
	if strings.Contains(response.Body.String(), nativeLeaseCapability) {
		t.Fatal("renewal response exposed the lease capability")
	}
}

func TestNativeWorkerRenewRejectsMissingOrInvalidAuthorityBeforeRenewal(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	authenticated := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{}, authenticated, noNativeLease,
		func(context.Context, string, nativeworker.RenewalRequest) (nativeworker.Renewal, error) {
			called = true
			return nativeworker.Renewal{}, nil
		},
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	for name, capability := range map[string]string{
		"missing": "",
		"short":   "not-authority",
		"unsafe":  strings.Repeat("x", 31) + "\t",
	} {
		t.Run(name, func(t *testing.T) {
			response := serveNativeRenew(t, handler, nativeRenewFixture(t), capability)
			var body apiError
			if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
				response.Code != http.StatusForbidden || body.Code != "CAPABILITY_MISMATCH" || called {
				t.Fatalf("status = %d, error = %#v, renewal called = %t", response.Code, body, called)
			}
		})
	}

	request := newNativeRenewRequest(t, nativeRenewFixture(t), nativeLeaseCapability)
	request.Header.Add("X-Perfeng-Lease", nativeLeaseCapability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body apiError
	if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		response.Code != http.StatusForbidden || body.Code != "CAPABILITY_MISMATCH" || called {
		t.Fatalf("duplicate capability status = %d, error = %#v, renewal called = %t", response.Code, body, called)
	}
}

func TestNativeWorkerRenewRequiresCurrentWorkerAuthorization(t *testing.T) {
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
		func(context.Context, string, nativeworker.RenewalRequest) (nativeworker.Renewal, error) {
			called = true
			return nativeworker.Renewal{}, nil
		},
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeRenew(t, handler, nativeRenewFixture(t), nativeLeaseCapability)
	var body apiError
	if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		response.Code != http.StatusForbidden || body.Code != "NOT_AUTHORIZED" || called {
		t.Fatalf("status = %d, error = %#v, renewal called = %t", response.Code, body, called)
	}
}

func TestNativeWorkerRenewRejectsInvalidRequestBeforeRenewal(t *testing.T) {
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
		func(context.Context, string, nativeworker.RenewalRequest) (nativeworker.Renewal, error) {
			called = true
			return nativeworker.Renewal{}, nil
		},
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		path     string
		document string
	}{
		"lease id": {"not-a-lease", string(nativeRenewFixture(t))},
		"body":     {nativeRenewalFixture(t).LeaseID, `{"attemptId":"bad","expectedRunRevision":4,"phase":"RUNNING"}`},
		"unknown":  {nativeRenewalFixture(t).LeaseID, `{"attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":4,"phase":"RUNNING","workerId":"chosen"}`},
	} {
		t.Run(name, func(t *testing.T) {
			request := newNativeRenewRequest(t, []byte(test.document), nativeLeaseCapability)
			request.URL.Path = nativeWorkerLeasePath + "/" + test.path + "/renew"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var body apiError
			if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
				response.Code != http.StatusBadRequest || body.Code != "INVALID_REQUEST" || called {
				t.Fatalf("status = %d, error = %#v, renewal called = %t", response.Code, body, called)
			}
		})
	}
}

func TestNativeWorkerRenewMapsAuthorityAndConcurrencyFailures(t *testing.T) {
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
		"phase":       {run.ErrConflict, http.StatusConflict, "REVISION_CONFLICT"},
		"unavailable": {run.ErrUnavailable, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE"},
		"internal":    {errors.New("storage failure"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, noNativeLease,
				func(context.Context, string, nativeworker.RenewalRequest) (nativeworker.Renewal, error) {
					return nativeworker.Renewal{}, test.err
				},
				noNativeRelease,
				noNativeUploadGrant,
				noNativeArtifactRegistration, noNativeTerminal)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeRenew(t, handler, nativeRenewFixture(t), nativeLeaseCapability)
			var body apiError
			if json.Unmarshal(response.Body.Bytes(), &body) != nil ||
				response.Code != test.status || body.Code != test.code {
				t.Fatalf("status = %d, error = %#v", response.Code, body)
			}
		})
	}
}

func TestNativeWorkerRenewalRouteIsExact(t *testing.T) {
	const leaseID = "11111111-1111-4111-8111-111111111111"
	for path, want := range map[string]bool{
		nativeWorkerLeasePath + "/" + leaseID + "/renew":           true,
		nativeWorkerLeasePath + "/not-a-uuid/renew":                true,
		nativeWorkerLeasePath + "/" + leaseID + "/release":         false,
		nativeWorkerLeasePath + "/" + leaseID + "/renew/duplicate": false,
		nativeWorkerLeasePath + "//renew":                          false,
	} {
		actualID, actual := nativeWorkerRenewalLeaseID(path)
		if actual != want || (actual && actualID == "") {
			t.Fatalf("path %q: id = %q, matched = %t", path, actualID, actual)
		}
	}
}

const nativeLeaseCapability = "example-not-a-real-lease-capability-0001"

func serveNativeRenew(
	t *testing.T,
	handler http.Handler,
	document []byte,
	capability string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newNativeRenewRequest(t, document, capability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func newNativeRenewRequest(
	t *testing.T,
	document []byte,
	capability string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		nativeWorkerLeasePath+"/"+nativeRenewalFixture(t).LeaseID+"/renew",
		strings.NewReader(string(document)),
	)
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("X-Perfeng-Lease", capability)
	}

	return request
}

func nativeRenewFixture(t *testing.T) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/renew.json")
	if err != nil {
		t.Fatal(err)
	}

	return document
}

func nativeRenewalFixture(t *testing.T) nativeworker.Renewal {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/renewal.json")
	if err != nil {
		t.Fatal(err)
	}
	var renewal nativeworker.Renewal
	if json.Unmarshal(document, &renewal) != nil {
		t.Fatal("invalid pinned renewal fixture")
	}

	return renewal
}
