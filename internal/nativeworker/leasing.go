package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	maximumCandidateBatch = 100
	// MaximumPresenceBytes is the Native Worker API presence document limit.
	MaximumPresenceBytes = 65536
)

// Candidate is one principal-owned Run considered by the privileged native
// scheduler. Run is an isolated snapshot.
type Candidate struct {
	Principal string
	Run       run.Run
}

// PresenceObservation is one database-timestamped worker report used for
// scheduling. Presence is contract validated and independently owned.
type PresenceObservation struct {
	WorkerID   string
	ObservedAt time.Time
	Presence   Presence
}

// Lease is an acquired native execution attempt. Capability is secret bearer
// material returned only to the worker; it must never be logged or persisted in
// plaintext.
type Lease struct {
	LeaseID           string    `json:"leaseId"`
	AttemptID         string    `json:"attemptId"`
	RunID             string    `json:"runId"`
	RunRevision       int64     `json:"runRevision"`
	IssuedAt          time.Time `json:"issuedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	RenewAfterSeconds int       `json:"renewAfterSeconds"`
	Plan
	Capability string `json:"-"`
}

// String identifies a lease without exposing its bearer capability.
func (lease Lease) String() string {
	return fmt.Sprintf("native lease %s for run %s", lease.LeaseID, lease.RunID)
}

// GoString identifies a lease in diagnostic formatting without exposing its
// bearer capability.
func (lease Lease) GoString() string {
	return lease.String()
}

// PlanResolver resolves the server-reviewed plan for an immutable native Run.
type PlanResolver interface {
	ResolveNativePlan(context.Context, string, run.Run) (Plan, error)
}

// LeaseStore is the privileged persistence boundary for native scheduling.
// Implementations must be safe for concurrent use and use an authoritative
// clock for presence freshness, capacity, and lease expiry.
type LeaseStore interface {
	// EligibleNativeWorker returns the worker's latest available, qualified,
	// authorized and fresh presence. Absence or ineligibility returns
	// run.ErrNotFound without distinguishing the reason.
	EligibleNativeWorker(
		ctx context.Context,
		workerID string,
		maximumAge time.Duration,
	) (PresenceObservation, error)

	// NativeCandidates returns at most limit CREATED native Runs. A short or
	// empty result is normal and does not establish lease eligibility.
	NativeCandidates(ctx context.Context, limit int) ([]Candidate, error)

	// AcquireNativeLease atomically rechecks the exact presence observation,
	// worker capacity, Run state and revision, and competing lease ownership.
	// acquired is false when any recheck loses a race or is no longer eligible.
	// A successful acquisition persists only a digest of Lease.Capability.
	AcquireNativeLease(
		ctx context.Context,
		workerID string,
		expectedPresenceAt time.Time,
		candidate Candidate,
		plan Plan,
		presenceMaximumAge time.Duration,
		ttl time.Duration,
		renewAfter time.Duration,
	) (lease Lease, acquired bool, err error)
}

// Acquirer selects a compatible reviewed plan and delegates the final fenced
// lease decision to durable storage.
type Acquirer struct {
	store              LeaseStore
	resolver           PlanResolver
	presenceMaximumAge time.Duration
	leaseTTL           time.Duration
	renewAfter         time.Duration
	candidateLimit     int
}

// NewAcquirer constructs a native lease selector with bounded scheduling and
// lease policy.
func NewAcquirer(
	store LeaseStore,
	resolver PlanResolver,
	presenceMaximumAge time.Duration,
	leaseTTL time.Duration,
	renewAfter time.Duration,
	candidateLimit int,
) (*Acquirer, error) {
	if store == nil || resolver == nil || !ValidPresenceMaximumAge(presenceMaximumAge) ||
		!ValidLeasePolicy(leaseTTL, renewAfter) || !ValidCandidateLimit(candidateLimit) {
		return nil, run.ErrValidation
	}

	return &Acquirer{
		store: store, resolver: resolver, presenceMaximumAge: presenceMaximumAge,
		leaseTTL: leaseTTL, renewAfter: renewAfter, candidateLimit: candidateLimit,
	}, nil
}

// Acquire returns one compatible durable lease or acquired=false when no work
// is currently eligible. Resolver and storage failures are returned unchanged.
func (acquirer *Acquirer) Acquire(
	ctx context.Context,
	workerID string,
) (lease Lease, acquired bool, err error) {
	if acquirer == nil || !contract.ValidNativeWorkerIdentifier(workerID) {
		return Lease{}, false, run.ErrValidation
	}
	presence, err := acquirer.store.EligibleNativeWorker(
		ctx,
		workerID,
		acquirer.presenceMaximumAge,
	)
	if errors.Is(err, run.ErrNotFound) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	if presence.WorkerID != workerID || presence.Presence.Availability != "AVAILABLE" {
		return Lease{}, false, run.ErrValidation
	}

	candidates, err := acquirer.store.NativeCandidates(ctx, acquirer.candidateLimit)
	if err != nil {
		return Lease{}, false, err
	}
	for _, candidate := range candidates {
		if candidate.Principal == "" || candidate.Run.State != run.StateCreated {
			return Lease{}, false, run.ErrValidation
		}
		plan, err := acquirer.resolver.ResolveNativePlan(ctx, candidate.Principal, candidate.Run)
		if err != nil {
			return Lease{}, false, err
		}
		if !presence.Presence.Capabilities.Supports(plan) {
			continue
		}
		lease, acquired, err := acquirer.store.AcquireNativeLease(
			ctx,
			workerID,
			presence.ObservedAt,
			candidate,
			plan,
			acquirer.presenceMaximumAge,
			acquirer.leaseTTL,
			acquirer.renewAfter,
		)
		if err != nil {
			return Lease{}, false, err
		}
		if acquired {
			return lease, true, nil
		}
	}

	return Lease{}, false, nil
}

// ValidPresenceMaximumAge accepts whole-second freshness windows from five
// seconds through five minutes.
func ValidPresenceMaximumAge(value time.Duration) bool {
	return value >= 5*time.Second && value <= 5*time.Minute && value%time.Second == 0
}

// ValidLeasePolicy accepts a contract-compatible TTL and a whole-second renewal
// interval of at least five seconds and no more than half the TTL.
func ValidLeasePolicy(ttl time.Duration, renewAfter time.Duration) bool {
	return run.ValidLeaseTTL(ttl) && renewAfter >= 5*time.Second &&
		renewAfter <= ttl/2 && renewAfter%time.Second == 0
}

// ValidCandidateLimit reports whether a scheduler batch is bounded.
func ValidCandidateLimit(limit int) bool {
	return limit >= 1 && limit <= maximumCandidateBatch
}
