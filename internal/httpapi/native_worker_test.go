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
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type nativePresenceStoreStub struct {
	record NativeWorkerPresenceRecord
	at     time.Time
	err    error
}

func TestNewNativeWorkerHandlerRequiresBothBoundaries(t *testing.T) {
	store := &nativePresenceStoreStub{}
	authenticate := func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{}, nil
	}
	if _, err := NewNativeWorkerHandler(nil, authenticate); err == nil {
		t.Fatal("missing presence store accepted")
	}
	if _, err := NewNativeWorkerHandler(store, nil); err == nil {
		t.Fatal("missing worker authentication accepted")
	}
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
	})
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
	})
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
			handler, err := NewNativeWorkerHandler(store, authenticate)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, nativeWorkerPresencePath, strings.NewReader(string(invalid)))
	request.Header.Set("Authorization", "Bearer worker-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || store.record.WorkerID != "" {
		t.Fatalf("status = %d, record = %#v", response.Code, store.record)
	}
}

func TestNativeWorkerPresenceClassifiesUnavailableStorage(t *testing.T) {
	document, evidence := nativePresenceFixture(t)
	store := &nativePresenceStoreStub{err: run.ErrUnavailable}
	handler, err := NewNativeWorkerHandler(store, func(context.Context, string) (WorkerIdentity, error) {
		return WorkerIdentity{WorkerID: "windows-laptop-01", QualificationEvidence: evidence}, nil
	})
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
