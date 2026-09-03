package run

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// ErrLeaseLost means a reconciliation lease is absent, expired, terminal, or
// owned by a different principal, worker, or token.
var ErrLeaseLost = errors.New("reconciliation lease is expired or no longer owned")

var workerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
var tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Lease is a worker-only capability. ExpiresAt is informational: storage
// validates ownership and expiry using its authoritative clock and stored row.
// Never expose or log Token as API metadata.
type Lease struct {
	RunID     string
	Principal string
	WorkerID  string
	Token     string
	ExpiresAt time.Time
}

// Valid checks the lease's identity and capability syntax. It does not establish
// current ownership; only a ReconciliationStore operation can do that.
func (l Lease) Valid() bool {
	return contract.ValidID(l.RunID) &&
		l.Principal != "" &&
		workerPattern.MatchString(l.WorkerID) &&
		tokenPattern.MatchString(l.Token)
}

// Claim combines the current Run snapshot with the lease authorizing its worker.
type Claim struct {
	Lease Lease
	Run   Run
}

// ValidClaimOptions accepts worker IDs of 1-128 permitted ASCII characters,
// batch sizes of 1-100, and a valid lease duration.
func ValidClaimOptions(workerID string, limit int, ttl time.Duration) bool {
	return workerPattern.MatchString(workerID) &&
		limit >= 1 &&
		limit <= 100 &&
		ValidLeaseTTL(ttl)
}

// ValidLeaseTTL accepts whole-second durations from 5 seconds through 5 minutes.
func ValidLeaseTTL(ttl time.Duration) bool {
	return ttl >= 5*time.Second &&
		ttl <= 5*time.Minute &&
		ttl%time.Second == 0
}

// ValidRetryDelay accepts whole-second durations from zero through 5 minutes.
func ValidRetryDelay(delay time.Duration) bool {
	return delay >= 0 &&
		delay <= 5*time.Minute &&
		delay%time.Second == 0
}

// ReconciliationStore is the privileged boundary through which workers discover
// and mutate runs across principals. Implementations must be safe for concurrent
// use, honor context cancellation, serialize each operation with cancellation
// and other lifecycle writes, and return snapshots isolated from stored state.
//
// Lease coordination never changes Run revisions. Lease expiry is decided from
// the store's clock and persisted lease, not Lease.ExpiresAt. Owned operations
// return ErrLeaseLost without revealing whether a run, principal, or lease
// exists. Persistence failures can return ErrUnavailable; a commit failure can
// have an uncertain outcome. Leases fence store writes, not external side effects.
type ReconciliationStore interface {
	// ClaimRuns returns at most limit eligible, nonterminal runs and leases them
	// atomically. Missing leases and expired leases whose delay elapsed are
	// eligible; CANCELLING bypasses delay but not a live lease. Every acquisition
	// gets a new token and an expiry based on a separate authoritative clock read.
	//
	// A successful empty or short slice is normal under contention and is not an
	// end-of-work signal. ErrValidation reports invalid workerID, limit, or ttl.
	// On error, no partial batch is returned.
	ClaimRuns(
		ctx context.Context,
		workerID string,
		limit int,
		ttl time.Duration,
	) ([]Claim, error)

	// RenewClaim validates persisted ownership after locking the Run, replaces
	// expiry with the authoritative current time plus ttl, and returns the latest
	// Run snapshot. It preserves the token and Run revision. ErrValidation reports
	// invalid inputs; ErrLeaseLost reports absent, expired, or terminal ownership.
	RenewClaim(ctx context.Context, lease Lease, ttl time.Duration) (Claim, error)

	// ReleaseClaim validates persisted ownership, expires it at the authoritative
	// current time, and delays normal rediscovery by retryDelay. A CANCELLING run
	// can bypass that delay. ErrValidation reports invalid inputs and ErrLeaseLost
	// reports ownership that is no longer current.
	ReleaseClaim(ctx context.Context, lease Lease, retryDelay time.Duration) error

	// AdvanceClaim validates persisted ownership before applying Change against
	// expectedRevision. It returns the same transition errors as Run.Transition.
	// A terminal transition expires the lease atomically with the Run update.
	// ErrLeaseLost takes precedence when ownership is not current.
	AdvanceClaim(
		ctx context.Context,
		lease Lease,
		expectedRevision int64,
		change Change,
	) (Run, error)
}
