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

func TestNativeWorkerArtifactRegistrationBindsAuthenticatedAuthority(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	want := nativeArtifactReferenceFixture(t)
	var calledWorker string
	var calledRequest nativeworker.ArtifactRegistrationRequest
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
		func(
			_ context.Context,
			workerID string,
			request nativeworker.ArtifactRegistrationRequest,
		) (nativeworker.ArtifactReference, error) {
			calledWorker = workerID
			calledRequest = request

			return want, nil
		}, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeArtifactRegistration(
		t,
		handler,
		nativeArtifactRegistrationFixture(t),
		nativeLeaseCapability,
	)
	if response.Code != http.StatusCreated || calledWorker != "linux-workstation-01" ||
		calledRequest.LeaseID != "11111111-1111-4111-8111-111111111111" ||
		calledRequest.AttemptID != want.AttemptID || calledRequest.ExpectedRunRevision != 5 ||
		calledRequest.GrantID != "44444444-4444-4444-8444-444444444444" ||
		calledRequest.Slot != want.Slot || calledRequest.Size != want.Size ||
		calledRequest.SHA256 != want.SHA256 || calledRequest.Capability != nativeLeaseCapability {
		t.Fatalf(
			"status = %d, worker = %q, request = %#v",
			response.Code,
			calledWorker,
			calledRequest,
		)
	}
	var actual map[string]any
	if json.Unmarshal(response.Body.Bytes(), &actual) != nil ||
		actual["artifactId"] != want.ArtifactID || actual["runId"] != want.RunID ||
		actual["uri"] != want.URI || actual["format"] != nil {
		t.Fatalf("artifact response = %#v", actual)
	}
	if strings.Contains(response.Body.String(), nativeLeaseCapability) ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v, body = %q", response.Header(), response.Body)
	}
}

func TestNativeWorkerArtifactRegistrationRejectsMissingOrInvalidAuthority(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
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
		func(
			context.Context,
			string,
			nativeworker.ArtifactRegistrationRequest,
		) (nativeworker.ArtifactReference, error) {
			called = true

			return nativeworker.ArtifactReference{}, nil
		}, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	for name, capability := range map[string]string{
		"missing": "",
		"short":   "not-authority",
		"unsafe":  strings.Repeat("x", 31) + "\t",
	} {
		t.Run(name, func(t *testing.T) {
			response := serveNativeArtifactRegistration(
				t, handler, nativeArtifactRegistrationFixture(t), capability,
			)
			assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
			if called {
				t.Fatal("artifact registration called without valid authority")
			}
		})
	}

	request := newNativeArtifactRegistrationRequest(
		t,
		nativeArtifactRegistrationFixture(t),
		nativeLeaseCapability,
	)
	request.Header.Add("X-Perfeng-Lease", nativeLeaseCapability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertNativeWorkerError(t, response, http.StatusForbidden, "CAPABILITY_MISMATCH")
	if called {
		t.Fatal("artifact registration called with duplicate capability headers")
	}
}

func TestNativeWorkerArtifactRegistrationRequiresAuthorization(t *testing.T) {
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
		func(
			context.Context,
			string,
			nativeworker.ArtifactRegistrationRequest,
		) (nativeworker.ArtifactReference, error) {
			called = true

			return nativeworker.ArtifactReference{}, nil
		}, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeArtifactRegistration(
		t,
		handler,
		nativeArtifactRegistrationFixture(t),
		nativeLeaseCapability,
	)
	assertNativeWorkerError(t, response, http.StatusForbidden, "NOT_AUTHORIZED")
	if called {
		t.Fatal("artifact registration called for unauthorized worker")
	}
}

func TestNativeWorkerArtifactRegistrationRejectsInvalidRequest(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
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
		func(
			context.Context,
			string,
			nativeworker.ArtifactRegistrationRequest,
		) (nativeworker.ArtifactReference, error) {
			called = true

			return nativeworker.ArtifactReference{}, nil
		}, noNativeTerminal)
	if err != nil {
		t.Fatal(err)
	}
	validDocument := nativeArtifactRegistrationFixture(t)
	var unknownValue map[string]any
	if json.Unmarshal(validDocument, &unknownValue) != nil {
		t.Fatal("invalid pinned artifact-registration fixture")
	}
	unknownValue["uri"] = "s3://worker-selected"
	unknownDocument, err := json.Marshal(unknownValue)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		leaseID string
		body    string
	}{
		"malformed": {leaseID: "11111111-1111-4111-8111-111111111111", body: `{"attemptId":`},
		"unknown": {
			leaseID: "11111111-1111-4111-8111-111111111111",
			body:    string(unknownDocument),
		},
		"lease": {leaseID: "not-a-lease", body: string(validDocument)},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				nativeWorkerLeasePath+"/"+test.leaseID+"/artifacts",
				strings.NewReader(test.body),
			)
			request.Header.Set("Authorization", "Bearer worker-token")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Perfeng-Lease", nativeLeaseCapability)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertNativeWorkerError(t, response, http.StatusBadRequest, "INVALID_REQUEST")
			if called {
				t.Fatal("artifact registration called for invalid request")
			}
		})
	}
}

func TestNativeWorkerArtifactRegistrationMapsFailures(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	valid := nativeArtifactReferenceFixture(t)
	invalid := valid
	invalid.Format = ""
	unencodable := valid
	unencodable.RegisteredAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	identity := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "linux-workstation-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	tests := map[string]struct {
		reference nativeworker.ArtifactReference
		err       error
		status    int
		code      string
	}{
		"lease":             {err: nativeworker.ErrLeaseGone, status: http.StatusGone, code: "LEASE_EXPIRED"},
		"grant":             {err: nativeworker.ErrUploadGrantMismatch, status: http.StatusConflict, code: "ARTIFACT_MISMATCH"},
		"plan":              {err: nativeworker.ErrArtifactPlan, status: http.StatusConflict, code: "ARTIFACT_MISMATCH"},
		"object":            {err: nativeworker.ErrArtifactVerification, status: http.StatusConflict, code: "ARTIFACT_MISMATCH"},
		"revision":          {err: run.ErrRevision, status: http.StatusConflict, code: "REVISION_CONFLICT"},
		"duplicate":         {err: run.ErrConflict, status: http.StatusConflict, code: "DUPLICATE_CONFLICT"},
		"validation":        {err: run.ErrValidation, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		"unavailable":       {err: run.ErrUnavailable, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"canceled":          {err: context.Canceled, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"deadline":          {err: context.DeadlineExceeded, status: http.StatusServiceUnavailable, code: "DEPENDENCY_UNAVAILABLE"},
		"internal":          {err: errors.New("safe internal failure"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		"invalid reference": {reference: invalid, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		"unencodable":       {reference: unencodable, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, noNativeLease, noNativeRenewal,
				noNativeRelease, noNativeUploadGrant,
				func(
					context.Context,
					string,
					nativeworker.ArtifactRegistrationRequest,
				) (nativeworker.ArtifactReference, error) {
					return test.reference, test.err
				}, noNativeTerminal)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeArtifactRegistration(
				t,
				handler,
				nativeArtifactRegistrationFixture(t),
				nativeLeaseCapability,
			)
			assertNativeWorkerError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "safe internal failure") {
				t.Fatal("artifact registration exposed dependency detail")
			}
		})
	}
}

func TestNativeWorkerArtifactRegistrationRouteIsExact(t *testing.T) {
	for path, want := range map[string]bool{
		"/native-worker/v2/leases/lease/artifacts":       true,
		"/native-worker/v2/leases//artifacts":            false,
		"/native-worker/v2/leases/lease/extra/artifacts": false,
		"/native-worker/v2/leases/lease/artifacts/":      false,
		"/native-worker/v2/leases/lease/upload-grants":   false,
	} {
		leaseID, actual := nativeWorkerArtifactLeaseID(path)
		if actual != want || actual && leaseID != "lease" {
			t.Fatalf("route %q = %q, %t", path, leaseID, actual)
		}
	}
}

func serveNativeArtifactRegistration(
	t *testing.T,
	handler http.Handler,
	document []byte,
	capability string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := newNativeArtifactRegistrationRequest(t, document, capability)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func newNativeArtifactRegistrationRequest(
	t *testing.T,
	document []byte,
	capability string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		nativeWorkerLeasePath+"/11111111-1111-4111-8111-111111111111/artifacts",
		strings.NewReader(string(document)),
	)
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("X-Perfeng-Lease", capability)
	}

	return request
}

func nativeArtifactRegistrationFixture(t *testing.T) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile(
		"snapshot/api/native-worker/v2/examples/register-artifact.json",
	)
	if err != nil {
		t.Fatal(err)
	}

	return document
}

func nativeArtifactReferenceFixture(t *testing.T) nativeworker.ArtifactReference {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference nativeworker.ArtifactReference
	if json.Unmarshal(document, &reference) != nil {
		t.Fatal("invalid pinned native artifact fixture")
	}
	reference.Format = "playwright-measurements/v2"
	if !reference.Valid() {
		t.Fatal("invalid native artifact reference fixture")
	}

	return reference
}
