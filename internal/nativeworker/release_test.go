package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type releaseStoreStub struct {
	workerID string
	request  ReleaseRequest
	err      error
}

func (store *releaseStoreStub) ReleaseNativeLease(
	_ context.Context,
	workerID string,
	request ReleaseRequest,
) error {
	store.workerID = workerID
	store.request = request

	return store.err
}

func validReleaseRequest() ReleaseRequest {
	return ReleaseRequest{
		LeaseID:             "11111111-1111-4111-8111-111111111111",
		AttemptID:           "22222222-2222-4222-8222-222222222222",
		ExpectedRunRevision: 5,
		Reason:              ReleaseCancellationRequested,
		Message:             "browser process tree stopped",
		Capability:          strings.Repeat("a", 64),
	}
}

func TestReleaserValidatesAndDelegatesAuthority(t *testing.T) {
	store := &releaseStoreStub{}
	releaser, err := NewReleaser(store)
	if err != nil {
		t.Fatal(err)
	}
	request := validReleaseRequest()
	if err = releaser.Release(context.Background(), "linux-workstation-01", request); err != nil ||
		store.workerID != "linux-workstation-01" || store.request != request {
		t.Fatalf("worker = %q, request = %#v, error = %v", store.workerID, store.request, err)
	}

	for name, mutate := range map[string]func(*ReleaseRequest){
		"lease":      func(value *ReleaseRequest) { value.LeaseID = "invalid" },
		"attempt":    func(value *ReleaseRequest) { value.AttemptID = "invalid" },
		"revision":   func(value *ReleaseRequest) { value.ExpectedRunRevision = 0 },
		"reason":     func(value *ReleaseRequest) { value.Reason = "UNKNOWN" },
		"message":    func(value *ReleaseRequest) { value.Message = strings.Repeat("x", 513) },
		"capability": func(value *ReleaseRequest) { value.Capability = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validReleaseRequest()
			mutate(&invalid)
			if err := releaser.Release(
				context.Background(), "linux-workstation-01", invalid,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReleaseRequestFormattingDoesNotExposeSensitiveValues(t *testing.T) {
	request := validReleaseRequest()
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, request.Capability) ||
			strings.Contains(formatted, request.Message) {
			t.Fatal("diagnostic formatting exposed release detail")
		}
	}
}

func TestReleaseReasonAndMessageValidation(t *testing.T) {
	for _, reason := range []ReleaseReason{
		ReleaseCancellationRequested,
		ReleaseLeaseLost,
		ReleaseWorkerShutdown,
		ReleasePreparationFailed,
	} {
		if !reason.Valid() {
			t.Fatalf("valid reason rejected: %s", reason)
		}
	}
	if ReleaseReason("UNKNOWN").Valid() {
		t.Fatal("unknown release reason accepted")
	}
	if !validReleaseMessage("") || !validReleaseMessage(strings.Repeat("x", 512)) ||
		validReleaseMessage(strings.Repeat("x", 513)) || validReleaseMessage("\xff") {
		t.Fatal("release message bounds are invalid")
	}
}

func TestNewReleaserRejectsMissingStore(t *testing.T) {
	if _, err := NewReleaser(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("error = %v", err)
	}
}
