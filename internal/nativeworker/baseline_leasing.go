package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// BaselineLease is one acquired v2 browser attempt. Capability is secret
// bearer material and must not appear in JSON or ordinary diagnostics.
type BaselineLease struct {
	LeaseID           string    `json:"leaseId"`
	AttemptID         string    `json:"attemptId"`
	RunID             string    `json:"runId"`
	RunRevision       int64     `json:"runRevision"`
	IssuedAt          time.Time `json:"issuedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	RenewAfterSeconds int       `json:"renewAfterSeconds"`
	BaselinePlan
	Capability string `json:"-"`
}

// Valid checks a complete v2 lease before it crosses the worker boundary. It
// does not prove current ownership or authorization to mutate the lease.
func (lease BaselineLease) Valid() bool {
	return ValidLeaseIdentifier(lease.LeaseID) &&
		ValidLeaseIdentifier(lease.AttemptID) &&
		contract.ValidID(lease.RunID) && lease.RunRevision >= 1 &&
		!lease.IssuedAt.IsZero() && !lease.ExpiresAt.IsZero() &&
		ValidLeasePolicy(lease.ExpiresAt.Sub(lease.IssuedAt),
			time.Duration(lease.RenewAfterSeconds)*time.Second) &&
		lease.BaselinePlan.Validate() == nil && ValidLeaseCapability(lease.Capability)
}

// String identifies the lease without disclosing its bearer capability.
func (lease BaselineLease) String() string {
	return fmt.Sprintf("native baseline lease %s for run %s", lease.LeaseID, lease.RunID)
}

// GoString identifies the lease in diagnostic formatting without disclosing
// its bearer capability.
func (lease BaselineLease) GoString() string {
	return lease.String()
}

// BaselineLeaseStore is the privileged persistence boundary for v2 scheduling.
// Reads are advisory; acquisition must recheck all eligibility under locks
// using the database clock and persist only the capability digest.
type BaselineLeaseStore interface {
	// EligibleNativeWorkerV2 returns one fresh, qualified and authorized v2
	// observation. Missing or ineligible presence returns run.ErrNotFound.
	EligibleNativeWorkerV2(context.Context, string, time.Duration) (PresenceObservation, error)

	// NativeCandidates returns at most limit CREATED native Run snapshots.
	NativeCandidates(context.Context, int) ([]Candidate, error)

	// AcquireNativeBaselineLease rechecks the exact observation, reviewed plan,
	// Run revision, worker capacity and shared Run authority atomically.
	// acquired=false means a concurrent or eligibility recheck lost.
	AcquireNativeBaselineLease(
		context.Context, string, time.Time, Candidate, BaselinePlan,
		time.Duration, time.Duration, time.Duration,
	) (BaselineLease, bool, error)
}

// BaselineAcquirer selects reviewed compatible v2 work and delegates final
// authority to the generation-aware lease transaction.
type BaselineAcquirer struct {
	store              BaselineLeaseStore
	resolver           BaselinePlanResolver
	presenceMaximumAge time.Duration
	leaseTTL           time.Duration
	renewAfter         time.Duration
	candidateLimit     int
}

// NewBaselineAcquirer validates bounded v2 scheduling and lease policy.
func NewBaselineAcquirer(
	store BaselineLeaseStore,
	resolver BaselinePlanResolver,
	presenceMaximumAge time.Duration,
	leaseTTL time.Duration,
	renewAfter time.Duration,
	candidateLimit int,
) (*BaselineAcquirer, error) {
	if store == nil || resolver == nil || !ValidPresenceMaximumAge(presenceMaximumAge) ||
		!ValidLeasePolicy(leaseTTL, renewAfter) || !ValidCandidateLimit(candidateLimit) {
		return nil, run.ErrValidation
	}

	return &BaselineAcquirer{
		store: store, resolver: resolver, presenceMaximumAge: presenceMaximumAge,
		leaseTTL: leaseTTL, renewAfter: renewAfter, candidateLimit: candidateLimit,
	}, nil
}

// Acquire returns one durable v2 lease or acquired=false when no work is
// eligible. Storage and registry failures remain distinct from no work.
func (acquirer *BaselineAcquirer) Acquire(
	ctx context.Context,
	workerID string,
) (BaselineLease, bool, error) {
	if acquirer == nil || !contract.ValidNativeWorkerIdentifier(workerID) {
		return BaselineLease{}, false, run.ErrValidation
	}
	observation, err := acquirer.store.EligibleNativeWorkerV2(
		ctx, workerID, acquirer.presenceMaximumAge,
	)
	if errors.Is(err, run.ErrNotFound) {
		return BaselineLease{}, false, nil
	}
	if err != nil {
		return BaselineLease{}, false, err
	}
	if observation.WorkerID != workerID || observation.Presence.ProtocolVersion != 2 ||
		observation.Presence.Availability != "AVAILABLE" {
		return BaselineLease{}, false, run.ErrValidation
	}
	candidates, err := acquirer.store.NativeCandidates(ctx, acquirer.candidateLimit)
	if err != nil {
		return BaselineLease{}, false, err
	}
	for len(candidates) > 0 {
		selection, found, err := SelectBaselineCandidate(
			ctx, observation.Presence, candidates, acquirer.resolver,
		)
		if err != nil {
			return BaselineLease{}, false, err
		}
		if !found {
			break
		}
		lease, acquired, err := acquirer.store.AcquireNativeBaselineLease(
			ctx, workerID, observation.ObservedAt, selection.Candidate, selection.Plan,
			acquirer.presenceMaximumAge, acquirer.leaseTTL, acquirer.renewAfter,
		)
		if err != nil || acquired {
			return lease, acquired, err
		}
		candidates = candidates[selection.Index+1:]
	}

	return BaselineLease{}, false, nil
}
