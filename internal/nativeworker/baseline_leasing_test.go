package nativeworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type baselineLeaseStoreStub struct {
	observation  PresenceObservation
	presenceErr  error
	candidates   []Candidate
	candidateErr error
	attempts     []string
	selectedID   string
	lease        BaselineLease
}

func (stub *baselineLeaseStoreStub) EligibleNativeWorkerV2(
	_ context.Context, _ string, _ time.Duration,
) (PresenceObservation, error) {
	return stub.observation, stub.presenceErr
}

func (stub *baselineLeaseStoreStub) NativeCandidates(
	_ context.Context, _ int,
) ([]Candidate, error) {
	return stub.candidates, stub.candidateErr
}

func (stub *baselineLeaseStoreStub) AcquireNativeBaselineLease(
	_ context.Context, _ string, _ time.Time, candidate Candidate, _ BaselinePlan,
	_ time.Duration, _ time.Duration, _ time.Duration,
) (BaselineLease, bool, error) {
	stub.attempts = append(stub.attempts, candidate.Run.ID)
	if candidate.Run.ID == stub.selectedID {
		return stub.lease, true, nil
	}

	return BaselineLease{}, false, nil
}

func baselinePlanFixture(t *testing.T) BaselinePlan {
	t.Helper()
	plan, err := baselineTemplateFixture().Resolve(
		"search-journey",
		rawresult.Identity{ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64)},
		"smoke", "local-demo", baselineArtifactFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return plan
}

func baselinePresenceFixture(t *testing.T) PresenceObservation {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParseV2Presence(document, MaximumPresenceBytes)
	if err != nil {
		t.Fatal(err)
	}

	return PresenceObservation{
		WorkerID: "linux-workstation-01", ObservedAt: time.Now().UTC(), Presence: presence,
	}
}

func TestBaselineAcquirerSkipsIncompatibleAndContendedCandidates(t *testing.T) {
	plan := baselinePlanFixture(t)
	incompatible := plan
	incompatible.Assignment.Recipe.Environment.EvidenceSHA256 = strings.Repeat("d", 64)
	store := &baselineLeaseStoreStub{
		observation: baselinePresenceFixture(t),
		candidates: []Candidate{
			{Principal: "alice", Run: run.Run{ID: "v1-only", State: run.StateCreated}},
			{Principal: "alice", Run: run.Run{ID: "wrong-host", State: run.StateCreated}},
			{Principal: "alice", Run: run.Run{ID: "contended", State: run.StateCreated}},
			{Principal: "alice", Run: run.Run{ID: "selected", State: run.StateCreated}},
		},
		selectedID: "selected", lease: BaselineLease{LeaseID: "selected-lease"},
	}
	resolver := baselineResolverStub{
		plans: map[string]BaselinePlan{
			"wrong-host": incompatible, "contended": plan, "selected": plan,
		},
		errs: map[string]error{"v1-only": run.ErrForbidden},
	}
	acquirer, err := NewBaselineAcquirer(
		store, resolver, 2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := acquirer.Acquire(context.Background(), "linux-workstation-01")
	if err != nil || !acquired || lease.LeaseID != "selected-lease" {
		t.Fatalf("acquisition = (%#v, %t, %v)", lease, acquired, err)
	}
	if len(store.attempts) != 2 || store.attempts[0] != "contended" ||
		store.attempts[1] != "selected" {
		t.Fatalf("acquisition attempts = %v", store.attempts)
	}
}

func TestBaselineAcquirerKeepsAbsenceDistinctFromFailures(t *testing.T) {
	store := &baselineLeaseStoreStub{presenceErr: run.ErrNotFound}
	acquirer, err := NewBaselineAcquirer(
		store, baselineResolverStub{}, 2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := acquirer.Acquire(context.Background(), "linux-workstation-01"); err != nil || acquired {
		t.Fatalf("missing worker = (%t, %v)", acquired, err)
	}
	failure := errors.New("candidate read failed")
	store.presenceErr = nil
	store.observation = baselinePresenceFixture(t)
	store.candidateErr = failure
	if _, _, err := acquirer.Acquire(context.Background(), "linux-workstation-01"); !errors.Is(err, failure) {
		t.Fatalf("storage failure = %v", err)
	}
}

func TestBaselineLeaseIsContractValidAndRedactsCapability(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/lease.json")
	if err != nil {
		t.Fatal(err)
	}
	var lease BaselineLease
	if err := json.Unmarshal(document, &lease); err != nil {
		t.Fatal(err)
	}
	lease.Capability = strings.Repeat("Z", 64)
	if !lease.Valid() {
		t.Fatal("pinned v2 lease was rejected")
	}
	wire, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(wire))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateNativeWorkerV2Lease(value); err != nil {
		t.Fatalf("lease violates pinned v2 contract: %v", err)
	}
	if strings.Contains(string(wire), lease.Capability) ||
		strings.Contains(lease.String(), lease.Capability) ||
		strings.Contains(lease.GoString(), lease.Capability) {
		t.Fatal("lease capability exposed in diagnostic or JSON output")
	}
}
