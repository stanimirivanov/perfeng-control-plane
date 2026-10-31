package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeWorkerTerminalBindsAuthenticatedAuthority(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	want := nativeworker.TerminalAcknowledgement{
		AttemptID:   "22222222-2222-4222-8222-222222222222",
		RunRevision: 7, Disposition: nativeworker.TerminalAccepted,
	}
	var calledWorker string
	var calledRequest nativeworker.TerminalRequest
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "linux-workstation-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
		func(
			_ context.Context,
			workerID string,
			request nativeworker.TerminalRequest,
		) (nativeworker.TerminalAcknowledgement, error) {
			calledWorker = workerID
			calledRequest = request

			return want, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeTerminal(
		t,
		handler,
		nativeTerminalFixture(t, "terminal-completed.json"),
		nativeLeaseCapability,
	)
	wantRequest := nativeworker.TerminalRequest{
		Kind:      nativeworker.TerminalCompleted,
		LeaseID:   "11111111-1111-4111-8111-111111111111",
		AttemptID: want.AttemptID, ExpectedRunRevision: 6,
		ManifestArtifactID: "33333333-3333-4333-8333-333333333333",
		Capability:         nativeLeaseCapability,
	}
	if response.Code != http.StatusOK || calledWorker != "linux-workstation-01" ||
		!reflect.DeepEqual(calledRequest, wantRequest) {
		t.Fatalf(
			"status = %d, worker = %q, request = %#v",
			response.Code,
			calledWorker,
			calledRequest,
		)
	}
	var acknowledgement nativeworker.TerminalAcknowledgement
	var value any
	decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
	decoder.UseNumber()
	if json.Unmarshal(response.Body.Bytes(), &acknowledgement) != nil || acknowledgement != want ||
		decoder.Decode(&value) != nil ||
		contract.ValidateNativeWorkerV2TerminalAcknowledgement(value) != nil {
		t.Fatalf("terminal acknowledgement = %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), nativeLeaseCapability) ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v, body = %q", response.Header(), response.Body)
	}
}

func TestNativeWorkerTerminalBindsFailedClassification(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	var called nativeworker.TerminalRequest
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "linux-workstation-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
		func(
			_ context.Context,
			_ string,
			request nativeworker.TerminalRequest,
		) (nativeworker.TerminalAcknowledgement, error) {
			called = request

			return nativeworker.TerminalAcknowledgement{
				AttemptID: request.AttemptID, RunRevision: 7,
				Disposition: nativeworker.TerminalAccepted,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	response := serveNativeTerminal(
		t,
		handler,
		nativeTerminalFixture(t, "terminal-failed.json"),
		nativeLeaseCapability,
	)
	if response.Code != http.StatusOK || called.Kind != nativeworker.TerminalFailed ||
		called.ManifestArtifactID != "" || called.Failure == nil ||
		called.Failure.Code != nativeworker.TerminalFailureTool || called.Failure.Retryable ||
		called.Failure.Message != "The reviewed runner exited unsuccessfully." {
		t.Fatalf("status = %d, request = %#v", response.Code, called)
	}
}

func TestNativeWorkerTerminalRejectsInvalidAuthorityAndInput(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	authenticate := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "linux-workstation-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{}, authenticate, noNativeLease, noNativeRenewal,
		noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration,
		func(
			context.Context,
			string,
			nativeworker.TerminalRequest,
		) (nativeworker.TerminalAcknowledgement, error) {
			called = true

			return nativeworker.TerminalAcknowledgement{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	valid := nativeTerminalFixture(t, "terminal-completed.json")
	for name, test := range map[string]struct {
		capability string
		leaseID    string
		body       string
		status     int
		code       string
	}{
		"missing capability": {
			leaseID: "11111111-1111-4111-8111-111111111111",
			body:    string(valid), status: http.StatusForbidden, code: "CAPABILITY_MISMATCH",
		},
		"invalid lease": {
			capability: nativeLeaseCapability, leaseID: "invalid", body: string(valid),
			status: http.StatusBadRequest, code: "INVALID_REQUEST",
		},
		"malformed": {
			capability: nativeLeaseCapability,
			leaseID:    "11111111-1111-4111-8111-111111111111",
			body:       `{"kind":`, status: http.StatusBadRequest, code: "INVALID_REQUEST",
		},
		"incomplete": {
			capability: nativeLeaseCapability,
			leaseID:    "11111111-1111-4111-8111-111111111111",
			body:       `{"kind":"COMPLETED","attemptId":"22222222-2222-4222-8222-222222222222","expectedRunRevision":6}`,
			status:     http.StatusBadRequest, code: "INVALID_REQUEST",
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				nativeWorkerLeasePath+"/"+test.leaseID+"/terminal",
				strings.NewReader(test.body),
			)
			request.Header.Set("Authorization", "Bearer worker-token")
			request.Header.Set("Content-Type", "application/json")
			if test.capability != "" {
				request.Header.Set("X-Perfeng-Lease", test.capability)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertNativeWorkerError(t, response, test.status, test.code)
			if called {
				t.Fatal("terminal reporting called for rejected request")
			}
		})
	}

	request := newNativeTerminalRequest(t, valid, nativeLeaseCapability)
	request.Header.Add("X-Perfeng-Lease", nativeLeaseCapability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
}

func TestNativeWorkerTerminalRequiresAuthorization(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "linux-workstation-01", QualificationEvidence: evidence,
			}, nil
		},
		noNativeLease,
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
		func(
			context.Context,
			string,
			nativeworker.TerminalRequest,
		) (nativeworker.TerminalAcknowledgement, error) {
			called = true

			return nativeworker.TerminalAcknowledgement{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	response := serveNativeTerminal(
		t,
		handler,
		nativeTerminalFixture(t, "terminal-completed.json"),
		nativeLeaseCapability,
	)
	assertNativeWorkerError(t, response, http.StatusForbidden, "NOT_AUTHORIZED")
	if called {
		t.Fatal("terminal reporting called for unauthorized worker")
	}
}

func TestNativeWorkerTerminalMapsFailures(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	valid := nativeworker.TerminalAcknowledgement{
		AttemptID:   "22222222-2222-4222-8222-222222222222",
		RunRevision: 7, Disposition: nativeworker.TerminalAccepted,
	}
	invalid := valid
	invalid.Disposition = "UNKNOWN"
	identity := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "linux-workstation-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	tests := map[string]struct {
		acknowledgement nativeworker.TerminalAcknowledgement
		err             error
		status          int
		code            string
	}{
		"lease":       {err: nativeworker.ErrLeaseGone, status: http.StatusGone, code: "LEASE_EXPIRED"},
		"evidence":    {err: nativeworker.ErrTerminalEvidence, status: http.StatusConflict, code: "ARTIFACT_MISMATCH"},
		"revision":    {err: run.ErrRevision, status: http.StatusConflict, code: "REVISION_CONFLICT"},
		"transition":  {err: run.ErrTransition, status: http.StatusConflict, code: "REVISION_CONFLICT"},
		"duplicate":   {err: nativeworker.ErrTerminalConflict, status: http.StatusConflict, code: "DUPLICATE_CONFLICT"},
		"phase":       {err: run.ErrConflict, status: http.StatusConflict, code: "REVISION_CONFLICT"},
		"validation":  {err: run.ErrValidation, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		"unavailable": {err: run.ErrUnavailable, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"canceled":    {err: context.Canceled, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"deadline":    {err: context.DeadlineExceeded, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"internal":    {err: errors.New("safe internal failure"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		"invalid ack": {acknowledgement: invalid, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, noNativeLease, noNativeRenewal,
				noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration,
				func(
					context.Context,
					string,
					nativeworker.TerminalRequest,
				) (nativeworker.TerminalAcknowledgement, error) {
					return test.acknowledgement, test.err
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeTerminal(
				t,
				handler,
				nativeTerminalFixture(t, "terminal-completed.json"),
				nativeLeaseCapability,
			)
			assertNativeWorkerError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "safe internal failure") {
				t.Fatal("terminal reporting exposed dependency detail")
			}
		})
	}
}

func TestNativeWorkerTerminalRouteIsExact(t *testing.T) {
	for path, want := range map[string]bool{
		"/native-worker/v2/leases/lease/terminal":       true,
		"/native-worker/v2/leases//terminal":            false,
		"/native-worker/v2/leases/lease/extra/terminal": false,
		"/native-worker/v2/leases/lease/terminal/":      false,
		"/native-worker/v2/leases/lease/artifacts":      false,
	} {
		leaseID, actual := nativeWorkerTerminalLeaseID(path)
		if actual != want || actual && leaseID != "lease" {
			t.Fatalf("route %q = %q, %t", path, leaseID, actual)
		}
	}
}

func serveNativeTerminal(
	t *testing.T,
	handler http.Handler,
	document []byte,
	capability string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newNativeTerminalRequest(t, document, capability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func newNativeTerminalRequest(
	t *testing.T,
	document []byte,
	capability string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		nativeWorkerLeasePath+"/11111111-1111-4111-8111-111111111111/terminal",
		strings.NewReader(string(document)),
	)
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("X-Perfeng-Lease", capability)
	}

	return request
}

func nativeTerminalFixture(t *testing.T, name string) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile(
		"snapshot/api/native-worker/v2/examples/" + name,
	)
	if err != nil {
		t.Fatal(err)
	}

	return document
}
