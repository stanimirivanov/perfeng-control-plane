package run

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

var ErrLeaseLost = errors.New("reconciliation lease is expired or no longer owned")

var workerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
var tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Lease is a worker-only capability. ExpiresAt is informational: the database
// validates the stored deadline. Never expose or log the token as API metadata.
type Lease struct {
	RunID     string
	Principal string
	WorkerID  string
	Token     string
	ExpiresAt time.Time
}

func (l Lease) Valid() bool {
	return contract.ValidID(l.RunID) && l.Principal != "" &&
		workerPattern.MatchString(l.WorkerID) && tokenPattern.MatchString(l.Token)
}

type Claim struct {
	Lease Lease
	Run   Run
}

func ValidClaimOptions(workerID string, limit int, ttl time.Duration) bool {
	return workerPattern.MatchString(workerID) && limit >= 1 && limit <= 100 && ValidLeaseTTL(ttl)
}
func ValidLeaseTTL(ttl time.Duration) bool {
	return ttl >= 5*time.Second && ttl <= 5*time.Minute && ttl%time.Second == 0
}
func ValidRetryDelay(delay time.Duration) bool {
	return delay >= 0 && delay <= 5*time.Minute && delay%time.Second == 0
}

// ReconciliationStore is a privileged worker boundary, not a tenant API.
// It discovers active runs across principals without changing run revisions.
// Only AdvanceClaim mutates a run; it checks BOTH lease ownership and revision.
// Leases fence database writes, NOT Kubernetes or other external side effects.
type ReconciliationStore interface {
	ClaimRuns(ctx context.Context, workerID string, limit int, ttl time.Duration) ([]Claim, error)
	RenewClaim(ctx context.Context, lease Lease, ttl time.Duration) (Claim, error)
	ReleaseClaim(ctx context.Context, lease Lease, retryDelay time.Duration) error
	AdvanceClaim(ctx context.Context, lease Lease, expectedRevision int64, change Change) (Run, error)
}
