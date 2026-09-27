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

type nativePresenceStoreStub struct {
	record NativeWorkerPresenceRecord
	at     time.Time
	err    error
}

func TestNewNativeWorkerHandlerRequiresAllBoundaries(t *testing.T) {
	store := &nativePresenceStoreStub{}
	authenticate := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{}, nil
	}
	if _, err := NewNativeWorkerHandler(nil, authenticate, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing presence store accepted")
	}
	if _, err := NewNativeWorkerHandler(store, nil, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing worker authentication accepted")
	}
	if _, err := NewNativeWorkerHandler(store, authenticate, nil, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing lease acquisition accepted")
	}
	if _, err := NewNativeWorkerHandler(store, authenticate, noNativeLease, nil, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing lease renewal accepted")
	}
	if _, err := NewNativeWorkerHandler(store, authenticate, noNativeLease, noNativeRenewal, nil, noNativeUploadGrant, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing lease release accepted")
	}
	if _, err := NewNativeWorkerHandler(store, authenticate, noNativeLease, noNativeRenewal, noNativeRelease, nil, noNativeArtifactRegistration); err == nil {
		t.Fatal("missing upload grant accepted")
	}
	if _, err := NewNativeWorkerHandler(store, authenticate, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, nil); err == nil {
		t.Fatal("missing artifact registration accepted")
	}
}

func noNativeLease(context.Context, string) (nativeworker.Lease, bool, error) {
	return nativeworker.Lease{}, false, nil
}

func noNativeRenewal(
	context.Context,
	string,
	nativeworker.RenewalRequest,
) (nativeworker.Renewal, error) {
	return nativeworker.Renewal{}, nil
}

func noNativeRelease(context.Context, string, nativeworker.ReleaseRequest) error {
	return nil
}

func noNativeUploadGrant(
	context.Context,
	string,
	nativeworker.UploadRequest,
) (nativeworker.UploadGrant, error) {
	return nativeworker.UploadGrant{}, nil
}

func noNativeArtifactRegistration(
	context.Context,
	string,
	nativeworker.ArtifactRegistrationRequest,
) (nativeworker.ArtifactReference, error) {
	return nativeworker.ArtifactReference{}, nil
}

func (store *nativePresenceStoreStub) RecordNativeWorkerPresence(
	_ context.Context,
	record NativeWorkerPresenceRecord,
) (time.Time, error) {
	store.record = record
	return store.at, store.err
}

func TestNativeWorkerPresenceAuthenticatesQualifiesAndPersists(t *testing.T) {
	document, evidence := nativePresenceFixture(t)
	observedAt := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	store := &nativePresenceStoreStub{at: observedAt}
	handler, err := NewNativeWorkerHandler(store, func(
		context.Context,
		string,
	) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(document)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var status workerPresenceStatus
	if json.Unmarshal(response.Body.Bytes(), &status) != nil ||
		status.WorkerID != "windows-laptop-01" || status.ServerTime != observedAt ||
		status.NextHeartbeat != nativeWorkerHeartbeat || status.Availability != "AVAILABLE" ||
		status.Qualification.Status != "QUALIFIED" || len(status.Qualification.Reasons) != 0 ||
		status.Qualification.ValidUntil == nil ||
		!status.Qualification.ValidUntil.Equal(observedAt.Add(nativeWorkerQualificationTTL)) ||
		!status.AuthorizedToAcquire {
		t.Fatalf("presence status = %#v", status)
	}
	if store.record.WorkerID != "windows-laptop-01" || !store.record.Qualified ||
		!store.record.AuthorizedToAcquire || store.record.Availability != "AVAILABLE" ||
		string(store.record.ObservedDocument) != string(document) {
		t.Fatalf("presence record = %#v", store.record)
	}
}

func TestNativeWorkerPresenceKeepsQualificationAndAuthorizationDistinct(t *testing.T) {
	document, evidence := nativePresenceFixture(t)
	var unapproved map[string]any
	if json.Unmarshal(evidence, &unapproved) != nil {
		t.Fatal("invalid qualification evidence fixture")
	}
	unapproved["environmentSha256"] = strings.Repeat("d", 64)
	unapprovedEvidence, err := json.Marshal(unapproved)
	if err != nil {
		t.Fatal(err)
	}
	store := &nativePresenceStoreStub{at: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	handler, err := NewNativeWorkerHandler(store, func(
		context.Context,
		string,
	) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: unapprovedEvidence,
		}, nil
	}, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(document)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var status workerPresenceStatus
	if json.Unmarshal(response.Body.Bytes(), &status) != nil ||
		status.Qualification.Status != "UNQUALIFIED" || len(status.Qualification.Reasons) != 1 ||
		status.Qualification.ValidUntil != nil || !status.AuthorizedToAcquire || store.record.Qualified {
		t.Fatalf("presence decision = %#v, record = %#v", status, store.record)
	}
}

func TestNativeWorkerPresenceRejectsInvalidTrustBoundaries(t *testing.T) {
	document, evidence := nativePresenceFixture(t)
	for name, configure := range map[string]func(*http.Request) NativeWorkerAuthenticate{
		"missing credential": func(*http.Request) NativeWorkerAuthenticate {
			return func(context.Context, string) (WorkerIdentity, error) {
				return WorkerIdentity{}, errors.New("not authenticated")
			}
		},
		"user audience": func(request *http.Request) NativeWorkerAuthenticate {
			request.Header.Set("Authorization", "Bearer user-token")
			return func(context.Context, string) (WorkerIdentity, error) {
				return WorkerIdentity{}, errors.New("wrong audience")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &nativePresenceStoreStub{}
			request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(document)))
			request.Header.Set("Content-Type", "application/json")
			authenticate := configure(request)
			handler, err := NewNativeWorkerHandler(
				store, authenticate, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant,
				noNativeArtifactRegistration,
			)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || store.record.WorkerID != "" {
				t.Fatalf("status = %d, record = %#v", response.Code, store.record)
			}
		})
	}

	var value map[string]any
	if json.Unmarshal(document, &value) != nil {
		t.Fatal("invalid test fixture")
	}
	value["workerId"] = "caller-selected"
	invalid, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	store := &nativePresenceStoreStub{}
	handler, err := NewNativeWorkerHandler(store, func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{WorkerID: "windows-laptop-01", QualificationEvidence: evidence}, nil
	}, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(invalid)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.record.WorkerID != "" {
		t.Fatalf("status = %d, record = %#v", response.Code, store.record)
	}
}

func TestNativeWorkerPresenceClassifiesUnavailableStorage(t *testing.T) {
	document, evidence := nativePresenceFixture(t)
	store := &nativePresenceStoreStub{err: run.ErrUnavailable}
	handler, err := NewNativeWorkerHandler(store, func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{WorkerID: "windows-laptop-01", QualificationEvidence: evidence}, nil
	}, noNativeLease, noNativeRenewal, noNativeRelease, noNativeUploadGrant, noNativeArtifactRegistration)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(document)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("status = %d, headers = %v", response.Code, response.Header())
	}
}

func TestNativeWorkerAcquireReturnsCapabilityOnlyInHeader(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	lease := nativeLeaseFixture(t)
	const capability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	lease.Capability = capability
	calledWorker := ""
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(_ context.Context, token string) (WorkerIdentity, error) {
			if token != "worker-token" {
				t.Fatal("authentication received a changed credential")
			}

			return WorkerIdentity{
				WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
				QualificationEvidence: evidence,
			}, nil
		},
		func(_ context.Context, workerID string) (nativeworker.Lease, bool, error) {
			calledWorker = workerID
			return lease, true, nil
		},
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeAcquire(t, handler, nativeAcquireFixture(t))
	if response.Code != http.StatusOK || calledWorker != "windows-laptop-01" ||
		response.Header().Get("X-Perfeng-Lease") != capability {
		t.Fatalf("status = %d, worker = %q, headers = %v", response.Code, calledWorker, response.Header())
	}
	if strings.Contains(response.Body.String(), capability) {
		t.Fatal("lease response body exposed the capability")
	}
	var actual nativeworker.Lease
	if json.Unmarshal(response.Body.Bytes(), &actual) != nil || actual.LeaseID != lease.LeaseID ||
		actual.AttemptID != lease.AttemptID || actual.RunID != lease.RunID || actual.Capability != "" {
		t.Fatalf("lease response = %#v", actual)
	}
}

func TestNativeWorkerAcquireReturnsBoundedNoWorkResponse(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
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
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeAcquire(t, handler, nativeAcquireFixture(t))
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 ||
		response.Header().Get("Retry-After") != "15" ||
		response.Header().Get("X-Perfeng-Lease") != "" {
		t.Fatalf("status = %d, headers = %v, body = %q", response.Code, response.Header(), response.Body)
	}
}

func TestNativeWorkerAcquireEnforcesAuthorizationBeforeScheduling(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	called := false
	handler, err := NewNativeWorkerHandler(
		&nativePresenceStoreStub{},
		func(context.Context, string) (WorkerIdentity, error) {
			return WorkerIdentity{
				WorkerID: "windows-laptop-01", QualificationEvidence: evidence,
			}, nil
		},
		func(context.Context, string) (nativeworker.Lease, bool, error) {
			called = true
			return nativeworker.Lease{}, false, nil
		},
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	response := serveNativeAcquire(t, handler, nativeAcquireFixture(t))
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("status = %d, acquisition called = %t", response.Code, called)
	}
}

func TestNativeWorkerAcquireRejectsInvalidRequestsBeforeScheduling(t *testing.T) {
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
		func(context.Context, string) (nativeworker.Lease, bool, error) {
			called = true
			return nativeworker.Lease{}, false, nil
		},
		noNativeRenewal,
		noNativeRelease,
		noNativeUploadGrant,
		noNativeArtifactRegistration,
	)
	if err != nil {
		t.Fatal(err)
	}

	for name, document := range map[string]string{
		"malformed": `{"protocolVersion":`,
		"version":   `{"protocolVersion":2,"maxWaitSeconds":20}`,
		"wait":      `{"protocolVersion":1,"maxWaitSeconds":31}`,
		"unknown":   `{"protocolVersion":1,"maxWaitSeconds":20,"workerId":"chosen"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := serveNativeAcquire(t, handler, []byte(document))
			if response.Code != http.StatusBadRequest || called {
				t.Fatalf("status = %d, acquisition called = %t", response.Code, called)
			}
		})
	}
}

func TestNativeWorkerAcquireRejectsFailuresAndInvalidLeases(t *testing.T) {
	_, evidence := nativePresenceFixture(t)
	unencodable := nativeLeaseFixture(t)
	unencodable.IssuedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	unencodable.ExpiresAt = unencodable.IssuedAt.Add(2 * time.Minute)
	unencodable.Capability = strings.Repeat("s", 64)
	identity := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{
			WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
			QualificationEvidence: evidence,
		}, nil
	}
	for name, acquire := range map[string]NativeWorkerAcquire{
		"unavailable": func(context.Context, string) (nativeworker.Lease, bool, error) {
			return nativeworker.Lease{}, false, run.ErrUnavailable
		},
		"internal": func(context.Context, string) (nativeworker.Lease, bool, error) {
			return nativeworker.Lease{}, false, run.ErrValidation
		},
		"invalid lease": func(context.Context, string) (nativeworker.Lease, bool, error) {
			return nativeworker.Lease{Capability: strings.Repeat("s", 64)}, true, nil
		},
		"unencodable lease": func(context.Context, string) (nativeworker.Lease, bool, error) {
			return unencodable, true, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := NewNativeWorkerHandler(
				&nativePresenceStoreStub{}, identity, acquire, noNativeRenewal, noNativeRelease,
				noNativeUploadGrant,
				noNativeArtifactRegistration,
			)
			if err != nil {
				t.Fatal(err)
			}
			response := serveNativeAcquire(t, handler, nativeAcquireFixture(t))
			want := http.StatusInternalServerError
			if name == "unavailable" {
				want = http.StatusServiceUnavailable
			}
			if response.Code != want || response.Header().Get("X-Perfeng-Lease") != "" {
				t.Fatalf("status = %d, headers = %v", response.Code, response.Header())
			}
		})
	}
}

func serveNativeAcquire(
	t *testing.T,
	handler http.Handler,
	document []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, nativeWorkerLeasePath, strings.NewReader(string(document)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	return response
}

func nativeAcquireFixture(t *testing.T) []byte {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/acquire.json")
	if err != nil {
		t.Fatal(err)
	}

	return document
}

func nativeLeaseFixture(t *testing.T) nativeworker.Lease {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/lease.json")
	if err != nil {
		t.Fatal(err)
	}
	var lease nativeworker.Lease
	if json.Unmarshal(document, &lease) != nil {
		t.Fatal("invalid pinned lease fixture")
	}

	return lease
}

func nativePresenceFixture(t *testing.T) ([]byte, json.RawMessage) {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(document, &value) != nil {
		t.Fatal("invalid pinned presence fixture")
	}
	evidence, err := json.Marshal(value["qualificationEvidence"])
	if err != nil {
		t.Fatal(err)
	}

	return document, evidence
}
