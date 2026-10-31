package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type renewalStoreStub struct {
	workerID          string
	request           RenewalRequest
	ttl               time.Duration
	cancellationGrace time.Duration
	renewal           Renewal
	err               error
}

func (store *renewalStoreStub) RenewNativeLease(
	_ context.Context,
	workerID string,
	request RenewalRequest,
	ttl time.Duration,
	cancellationGrace time.Duration,
) (Renewal, error) {
	store.workerID = workerID
	store.request = request
	store.ttl = ttl
	store.cancellationGrace = cancellationGrace

	return store.renewal, store.err
}

func validRenewalRequest() RenewalRequest {
	return RenewalRequest{
		LeaseID:             "11111111-1111-4111-8111-111111111111",
		AttemptID:           "22222222-2222-4222-8222-222222222222",
		ExpectedRunRevision: 4,
		Phase:               PhaseRunning,
		Capability:          strings.Repeat("a", 64),
	}
}

func TestRenewerValidatesAndDelegatesServerPolicy(t *testing.T) {
	store := &renewalStoreStub{renewal: Renewal{LeaseID: validRenewalRequest().LeaseID}}
	renewer, err := NewRenewer(store, 2*time.Minute, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request := validRenewalRequest()
	renewal, err := renewer.Renew(context.Background(), "linux-workstation-01", request)
	if err != nil || renewal.LeaseID != request.LeaseID ||
		store.workerID != "linux-workstation-01" || store.request != request ||
		store.ttl != 2*time.Minute || store.cancellationGrace != 30*time.Second {
		t.Fatalf("renewal lease = %q, worker = %q, ttl = %s, grace = %s, error = %v",
			renewal.LeaseID, store.workerID, store.ttl, store.cancellationGrace, err)
	}

	for name, mutate := range map[string]func(*RenewalRequest){
		"lease":      func(value *RenewalRequest) { value.LeaseID = "invalid" },
		"attempt":    func(value *RenewalRequest) { value.AttemptID = "invalid" },
		"revision":   func(value *RenewalRequest) { value.ExpectedRunRevision = 0 },
		"phase":      func(value *RenewalRequest) { value.Phase = "UNKNOWN" },
		"capability": func(value *RenewalRequest) { value.Capability = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validRenewalRequest()
			mutate(&invalid)
			if _, err := renewer.Renew(
				context.Background(), "linux-workstation-01", invalid,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRenewalRequestFormattingDoesNotExposeCapability(t *testing.T) {
	request := validRenewalRequest()
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, request.Capability) {
			t.Fatal("diagnostic formatting exposed the lease capability")
		}
	}
}

func TestNewRenewerRejectsInvalidPolicy(t *testing.T) {
	store := &renewalStoreStub{}
	for name, test := range map[string]struct {
		store RenewalStore
		ttl   time.Duration
		grace time.Duration
	}{
		"store": {ttl: 2 * time.Minute, grace: 30 * time.Second},
		"ttl":   {store: store, ttl: time.Second, grace: time.Second},
		"grace": {store: store, ttl: 2 * time.Minute, grace: 3 * time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRenewer(test.store, test.ttl, test.grace); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAdvancePhaseAppliesMonotonicLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	created := run.Run{State: run.StateCreated, Revision: 1, UpdatedAt: now.Add(-time.Minute)}
	preparing, err := AdvancePhase(created, 1, PhasePreparing, now)
	if err != nil || preparing.State != run.StateWarmingUp || preparing.Revision != 4 ||
		preparing.UpdatedAt != now {
		t.Fatalf("preparing = %#v, error = %v", preparing, err)
	}
	running, err := AdvancePhase(preparing, preparing.Revision, PhaseRunning, now.Add(time.Second))
	if err != nil || running.State != run.StateRunning || running.Revision != 5 {
		t.Fatalf("running = %#v, error = %v", running, err)
	}
	uploading, err := AdvancePhase(running, running.Revision, PhaseUploading, now.Add(2*time.Second))
	if err != nil || uploading.State != run.StateCollecting || uploading.Revision != 6 {
		t.Fatalf("uploading = %#v, error = %v", uploading, err)
	}
	finalizing, err := AdvancePhase(
		uploading, uploading.Revision, PhaseFinalizing, now.Add(3*time.Second),
	)
	if err != nil || finalizing != uploading {
		t.Fatalf("finalizing = %#v, error = %v", finalizing, err)
	}
}

func TestAdvancePhaseRejectsStaleAndRegressiveReports(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	current := run.Run{State: run.StateRunning, Revision: 5, UpdatedAt: now}
	if _, err := AdvancePhase(current, 4, PhaseRunning, now); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := AdvancePhase(current, 5, PhasePreparing, now); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("phase regression error = %v", err)
	}
	current.State = run.StateCancelling
	if _, err := AdvancePhase(current, 5, PhaseRunning, now); !errors.Is(err, run.ErrConflict) {
		t.Fatalf("cancelling phase error = %v", err)
	}
}

func TestRenewalValidationRequiresConsistentCancellation(t *testing.T) {
	serverTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	valid := Renewal{
		LeaseID:     "11111111-1111-4111-8111-111111111111",
		AttemptID:   "22222222-2222-4222-8222-222222222222",
		RunRevision: 5, ServerTime: serverTime, ExpiresAt: serverTime.Add(2 * time.Minute),
		RenewAfterSeconds: 40,
	}
	if !valid.Valid() {
		t.Fatal("valid renewal rejected")
	}
	stopBy := serverTime.Add(30 * time.Second)
	valid.CancellationRequested = true
	valid.StopBy = &stopBy
	if !valid.Valid() {
		t.Fatal("valid cancellation renewal rejected")
	}
	valid.StopBy = nil
	if valid.Valid() {
		t.Fatal("cancellation without deadline accepted")
	}
	valid.CancellationRequested = false
	valid.StopBy = &stopBy
	if valid.Valid() {
		t.Fatal("deadline without cancellation accepted")
	}
}
