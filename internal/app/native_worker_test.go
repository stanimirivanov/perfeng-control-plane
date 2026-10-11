package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type nativeV2StoreStub struct {
	NativeWorkerStore
	record        httpapi.NativeWorkerPresenceRecord
	releaseCalls  int
	inspectCalls  int
	discoverCalls int
}

func (store *nativeV2StoreStub) RecordNativeWorkerV2Presence(
	_ context.Context, record httpapi.NativeWorkerPresenceRecord,
) (time.Time, error) {
	store.record = record
	return time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC), nil
}

func (*nativeV2StoreStub) EligibleNativeWorkerV2(
	context.Context, string, time.Duration,
) (nativeworker.PresenceObservation, error) {
	return nativeworker.PresenceObservation{}, run.ErrNotFound
}

func (store *nativeV2StoreStub) ReleaseNativeBaselineLease(
	context.Context, string, nativeworker.ReleaseRequest,
) error {
	store.releaseCalls++
	return nil
}

func (store *nativeV2StoreStub) InspectNativeBaselineAttempt(
	context.Context, string, nativeworker.InspectionRequest,
) (nativeworker.Inspection, error) {
	store.inspectCalls++
	return nativeworker.Inspection{}, nativeworker.ErrAttemptNotFound
}

func (store *nativeV2StoreStub) DiscoverNativeBaselineAttempt(
	context.Context, string,
) (nativeworker.Discovery, error) {
	store.discoverCalls++
	return nativeworker.Discovery{
		State: nativeworker.DiscoveryNone, Availability: "UNREPORTED",
		ServerTime: time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC),
	}, nil
}

type nativeV2AuthenticatorStub struct{ evidence json.RawMessage }

func (stub nativeV2AuthenticatorStub) AuthenticateWorker(
	context.Context, string,
) (httpapi.WorkerIdentity, error) {
	return httpapi.WorkerIdentity{
		WorkerID: "windows-laptop-01", AuthorizedToAcquire: true,
		QualificationEvidence: stub.evidence,
	}, nil
}

type nativeV2ResolverStub struct{}

func (nativeV2ResolverStub) ResolveNativeBaselinePlan(
	context.Context, string, run.Run,
) (nativeworker.BaselinePlan, error) {
	return nativeworker.BaselinePlan{}, run.ErrForbidden
}

type nativeV2ArtifactVerifierStub struct{}

func (nativeV2ArtifactVerifierStub) VerifyNativeArtifact(
	context.Context, nativeworker.ArtifactCandidate,
) (nativeworker.VerifiedArtifact, error) {
	return nativeworker.VerifiedArtifact{}, run.ErrUnavailable
}

func TestNativeWorkerAcquisitionGate(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	var presence nativeworker.Presence
	if err := json.Unmarshal(document, &presence); err != nil {
		t.Fatal(err)
	}
	var evidenceValue any
	if err := json.Unmarshal(presence.QualificationEvidence, &evidenceValue); err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(evidenceValue)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		store := &nativeV2StoreStub{}
		handler, err := NewNativeWorkerAPIHandler(
			store, nativeV2AuthenticatorStub{evidence},
			nativeV2ResolverStub{}, startupUploadSignerStub{},
			nativeV2ArtifactVerifierStub{}, enabled,
		)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(
			http.MethodPost, "/native-worker/v2/presence", strings.NewReader(string(document)),
		)
		request.Header.Set("Authorization", "Bearer worker-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || store.record.AuthorizedToAcquire != enabled {
			t.Fatalf("gate=%t: status=%d, recorded authorization=%t", enabled,
				response.Code, store.record.AuthorizedToAcquire)
		}
		acquire := httptest.NewRequest(http.MethodPost, "/native-worker/v2/leases",
			strings.NewReader(`{"protocolVersion":2,"maxWaitSeconds":0}`))
		acquire.Header.Set("Authorization", "Bearer worker-token")
		acquire.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, acquire)
		want := http.StatusForbidden
		if enabled {
			want = http.StatusNoContent
		}
		if result.Code != want {
			t.Fatalf("gate=%t: acquisition status=%d, want=%d", enabled, result.Code, want)
		}
		releaseDocument, err := contract.Files.ReadFile(
			"snapshot/api/native-worker/v2/examples/release.json",
		)
		if err != nil {
			t.Fatal(err)
		}
		release := httptest.NewRequest(http.MethodPost,
			"/native-worker/v2/leases/11111111-1111-4111-8111-111111111111/release",
			strings.NewReader(string(releaseDocument)))
		release.Header.Set("Authorization", "Bearer worker-token")
		release.Header.Set("Content-Type", "application/json")
		release.Header.Set("X-Perfeng-Lease", strings.Repeat("a", 64))
		releaseResponse := httptest.NewRecorder()
		handler.ServeHTTP(releaseResponse, release)
		if releaseResponse.Code != http.StatusNoContent || store.releaseCalls != 1 {
			t.Fatalf("gate=%t: release status=%d, calls=%d", enabled,
				releaseResponse.Code, store.releaseCalls)
		}
	}
}

func TestNativeWorkerObservationRemainsAvailableWhenAcquisitionIsPaused(t *testing.T) {
	presenceDocument, err := contract.Files.ReadFile(
		"snapshot/api/native-worker/v2/examples/presence.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var presence nativeworker.Presence
	if err := json.Unmarshal(presenceDocument, &presence); err != nil {
		t.Fatal(err)
	}
	var evidenceValue any
	if err := json.Unmarshal(presence.QualificationEvidence, &evidenceValue); err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(evidenceValue)
	if err != nil {
		t.Fatal(err)
	}
	inspectDocument, err := contract.Files.ReadFile(
		"snapshot/api/native-worker/v2/examples/inspect-attempt.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		store := &nativeV2StoreStub{}
		handler, err := NewNativeWorkerAPIHandler(
			store, nativeV2AuthenticatorStub{evidence},
			nativeV2ResolverStub{}, startupUploadSignerStub{},
			nativeV2ArtifactVerifierStub{}, enabled,
		)
		if err != nil {
			t.Fatal(err)
		}
		inspect := httptest.NewRequest(http.MethodPost,
			"/native-worker/v2/attempts/inspect", strings.NewReader(string(inspectDocument)))
		inspect.Header.Set("Authorization", "Bearer worker-token")
		inspect.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, inspect)
		if response.Code != http.StatusNotFound || store.inspectCalls != 1 {
			t.Fatalf("gate=%t: inspection status=%d, calls=%d", enabled,
				response.Code, store.inspectCalls)
		}
		discover := httptest.NewRequest(http.MethodPost,
			"/native-worker/v2/attempts/discover", strings.NewReader(`{"protocolVersion":2}`))
		discover.Header.Set("Authorization", "Bearer worker-token")
		discover.Header.Set("Content-Type", "application/json")
		discoveryResponse := httptest.NewRecorder()
		handler.ServeHTTP(discoveryResponse, discover)
		if discoveryResponse.Code != http.StatusOK || store.discoverCalls != 1 {
			t.Fatalf("gate=%t: discovery status=%d, calls=%d", enabled,
				discoveryResponse.Code, store.discoverCalls)
		}
	}
}
