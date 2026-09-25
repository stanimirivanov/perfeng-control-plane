package nativeworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type leaseStoreStub struct {
	presence      PresenceObservation
	presenceErr   error
	candidates    []Candidate
	candidatesErr error
	attempts      []string
	acquireRunID  string
	lease         Lease
	acquireErr    error
}

func (store *leaseStoreStub) EligibleNativeWorker(
	_ context.Context,
	_ string,
	_ time.Duration,
) (PresenceObservation, error) {
	return store.presence, store.presenceErr
}

func (store *leaseStoreStub) NativeCandidates(
	_ context.Context,
	_ int,
) ([]Candidate, error) {
	return store.candidates, store.candidatesErr
}

func (store *leaseStoreStub) AcquireNativeLease(
	_ context.Context,
	_ string,
	_ time.Time,
	candidate Candidate,
	_ Plan,
	_ time.Duration,
	_ time.Duration,
	_ time.Duration,
) (Lease, bool, error) {
	store.attempts = append(store.attempts, candidate.Run.ID)
	if store.acquireErr != nil {
		return Lease{}, false, store.acquireErr
	}
	if candidate.Run.ID != store.acquireRunID {
		return Lease{}, false, nil
	}

	return store.lease, true, nil
}

type planResolverStub struct {
	plans map[string]Plan
	err   error
}

func (resolver planResolverStub) ResolveNativePlan(
	_ context.Context,
	_ string,
	current run.Run,
) (Plan, error) {
	if resolver.err != nil {
		return Plan{}, resolver.err
	}

	return resolver.plans[current.ID], nil
}

func nativePresenceObservation(t *testing.T) PresenceObservation {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParsePresence(document, MaximumPresenceBytes)
	if err != nil {
		t.Fatal(err)
	}

	return PresenceObservation{
		WorkerID: "windows-laptop-01", ObservedAt: time.Now().UTC(), Presence: presence,
	}
}

func nativePlan(t *testing.T, browser string) Plan {
	t.Helper()
	template := assignmentTemplate()
	template.Browser = browser
	plan, err := template.Resolve(
		"search-journey", "browser-smoke", "windows-mainstream", "local-demo", sourceCheckout(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return plan
}

func TestAcquirerSkipsIncompatibleAndContendedCandidates(t *testing.T) {
	store := &leaseStoreStub{
		presence: nativePresenceObservation(t),
		candidates: []Candidate{
			{Principal: "alice", Run: run.Run{ID: "incompatible", State: run.StateCreated}},
			{Principal: "alice", Run: run.Run{ID: "contended", State: run.StateCreated}},
			{Principal: "alice", Run: run.Run{ID: "selected", State: run.StateCreated}},
		},
		acquireRunID: "selected",
		lease:        Lease{LeaseID: "selected-lease"},
	}
	resolver := planResolverStub{plans: map[string]Plan{
		"incompatible": nativePlan(t, "chrome"),
		"contended":    nativePlan(t, "edge"),
		"selected":     nativePlan(t, "edge"),
	}}
	acquirer, err := NewAcquirer(
		store, resolver, 2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := acquirer.Acquire(context.Background(), "windows-laptop-01")
	if err != nil || !acquired || lease.LeaseID != "selected-lease" {
		t.Fatal("compatible lease was not acquired", acquired, lease, err)
	}
	if len(store.attempts) != 2 || store.attempts[0] != "contended" ||
		store.attempts[1] != "selected" {
		t.Fatalf("acquisition attempts = %v", store.attempts)
	}
}

func TestAcquirerTreatsIneligiblePresenceAsNoWork(t *testing.T) {
	store := &leaseStoreStub{presenceErr: run.ErrNotFound}
	acquirer, err := NewAcquirer(
		store, planResolverStub{}, 2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := acquirer.Acquire(context.Background(), "windows-laptop-01")
	if err != nil || acquired || lease.LeaseID != "" {
		t.Fatal("ineligible presence did not produce an empty acquisition", lease, acquired, err)
	}
}

func TestAcquirerReturnsResolverAndStorageFailures(t *testing.T) {
	expected := errors.New("resolution failed")
	store := &leaseStoreStub{
		presence: nativePresenceObservation(t),
		candidates: []Candidate{
			{Principal: "alice", Run: run.Run{ID: "candidate", State: run.StateCreated}},
		},
	}
	acquirer, err := NewAcquirer(
		store, planResolverStub{err: expected},
		2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = acquirer.Acquire(context.Background(), "windows-laptop-01"); !errors.Is(err, expected) {
		t.Fatalf("resolver error = %v", err)
	}

	store.candidatesErr = expected
	acquirer, err = NewAcquirer(
		store, planResolverStub{}, 2*time.Minute, 2*time.Minute, 40*time.Second, 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = acquirer.Acquire(context.Background(), "windows-laptop-01"); !errors.Is(err, expected) {
		t.Fatalf("candidate storage error = %v", err)
	}
}

func TestAcquirerRejectsInvalidConfigurationAndWorkerIdentity(t *testing.T) {
	store := &leaseStoreStub{}
	resolver := planResolverStub{}
	for _, test := range []struct {
		name       string
		store      LeaseStore
		resolver   PlanResolver
		maximumAge time.Duration
		ttl        time.Duration
		renewAfter time.Duration
		limit      int
	}{
		{name: "store", resolver: resolver, maximumAge: time.Minute, ttl: time.Minute, renewAfter: 20 * time.Second, limit: 1},
		{name: "resolver", store: store, maximumAge: time.Minute, ttl: time.Minute, renewAfter: 20 * time.Second, limit: 1},
		{name: "presence age", store: store, resolver: resolver, maximumAge: time.Millisecond, ttl: time.Minute, renewAfter: 20 * time.Second, limit: 1},
		{name: "lease TTL", store: store, resolver: resolver, maximumAge: time.Minute, ttl: time.Second, renewAfter: time.Second, limit: 1},
		{name: "renewal", store: store, resolver: resolver, maximumAge: time.Minute, ttl: time.Minute, renewAfter: 40 * time.Second, limit: 1},
		{name: "candidate limit", store: store, resolver: resolver, maximumAge: time.Minute, ttl: time.Minute, renewAfter: 20 * time.Second, limit: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAcquirer(
				test.store, test.resolver, test.maximumAge, test.ttl, test.renewAfter, test.limit,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("configuration error = %v", err)
			}
		})
	}

	acquirer, err := NewAcquirer(
		store, resolver, time.Minute, time.Minute, 20*time.Second, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := acquirer.Acquire(context.Background(), "INVALID"); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("worker identity error = %v", err)
	}
}

func TestLeaseFormattingAndJSONDoNotExposeCapability(t *testing.T) {
	const secret = "native-lease-secret-capability"
	lease := Lease{
		LeaseID:    "11111111-1111-4111-8111-111111111111",
		RunID:      "perf-20260918-120000-a1b2c3d4",
		Capability: secret,
	}
	for _, formatted := range []string{fmt.Sprint(lease), fmt.Sprintf("%#v", lease)} {
		if strings.Contains(formatted, secret) {
			t.Fatal("diagnostic formatting exposed the lease capability")
		}
	}
	encoded, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("lease JSON exposed the bearer capability")
	}
}

func TestLeaseValidation(t *testing.T) {
	issuedAt := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	valid := Lease{
		LeaseID: "11111111-1111-4111-8111-111111111111", AttemptID: "22222222-2222-4222-8222-222222222222",
		RunID: "perf-20260918-120000-a1b2c3d4", RunRevision: 4,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(2 * time.Minute), RenewAfterSeconds: 40,
		Plan: nativePlan(t, "edge"), Capability: strings.Repeat("a", 64),
	}
	if !valid.Valid() {
		t.Fatal("valid native lease rejected")
	}

	for name, mutate := range map[string]func(*Lease){
		"lease ID":   func(lease *Lease) { lease.LeaseID = "not-a-uuid" },
		"attempt ID": func(lease *Lease) { lease.AttemptID = "not-a-uuid" },
		"run":        func(lease *Lease) { lease.RunID = "invalid" },
		"revision":   func(lease *Lease) { lease.RunRevision = 0 },
		"expiry":     func(lease *Lease) { lease.ExpiresAt = lease.IssuedAt },
		"renewal":    func(lease *Lease) { lease.RenewAfterSeconds = 61 },
		"plan":       func(lease *Lease) { lease.Assignment.Browser = "firefox" },
		"capability": func(lease *Lease) { lease.Capability = strings.Repeat("a", 31) },
		"header":     func(lease *Lease) { lease.Capability = strings.Repeat("a", 32) + "\r\n" },
	} {
		t.Run(name, func(t *testing.T) {
			lease := valid
			lease.Plan = valid.Plan.Clone()
			mutate(&lease)
			if lease.Valid() {
				t.Fatal("invalid native lease accepted")
			}
		})
	}
}
