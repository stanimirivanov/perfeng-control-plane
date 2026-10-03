package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrLeaseGone means native lease authority is absent, expired, released or
// no longer belongs to the authenticated worker and attempt.
var ErrLeaseGone = errors.New("native lease is no longer current")

// Phase is the worker-observed execution phase reported during renewal.
type Phase string

const (
	// PhasePreparing means the worker is preparing the browser measurement.
	PhasePreparing Phase = "PREPARING"
	// PhaseRunning means the browser measurement window is active.
	PhaseRunning Phase = "RUNNING"
	// PhaseUploading means the worker is publishing captured evidence.
	PhaseUploading Phase = "UPLOADING"
	// PhaseFinalizing means the worker is completing evidence registration.
	PhaseFinalizing Phase = "FINALIZING"
)

// Valid reports whether phase belongs to Native Worker API v1 or v2.
func (phase Phase) Valid() bool {
	switch phase {
	case PhasePreparing, PhaseRunning, PhaseUploading, PhaseFinalizing:
		return true
	default:
		return false
	}
}

// RenewalRequest contains the complete authority and revision fence for one
// native lease renewal. Capability must never be logged or persisted.
type RenewalRequest struct {
	LeaseID             string
	AttemptID           string
	ExpectedRunRevision int64
	Phase               Phase
	Capability          string
}

// Valid checks the renewal authority and phase syntax. It does not establish
// current ownership; only RenewalStore can do that.
func (request RenewalRequest) Valid() bool {
	return ValidLeaseIdentifier(request.LeaseID) &&
		ValidLeaseIdentifier(request.AttemptID) && request.ExpectedRunRevision >= 1 &&
		request.Phase.Valid() && ValidLeaseCapability(request.Capability)
}

// String identifies a renewal without exposing its bearer capability.
func (request RenewalRequest) String() string {
	return fmt.Sprintf("native lease renewal %s attempt %s", request.LeaseID, request.AttemptID)
}

// GoString identifies a renewal in diagnostic formatting without exposing its
// bearer capability.
func (request RenewalRequest) GoString() string {
	return request.String()
}

// Renewal is the authoritative lease timing, Run revision and cancellation
// decision returned after a committed renewal.
type Renewal struct {
	LeaseID               string     `json:"leaseId"`
	AttemptID             string     `json:"attemptId"`
	RunRevision           int64      `json:"runRevision"`
	ServerTime            time.Time  `json:"serverTime"`
	ExpiresAt             time.Time  `json:"expiresAt"`
	RenewAfterSeconds     int        `json:"renewAfterSeconds"`
	CancellationRequested bool       `json:"cancellationRequested"`
	StopBy                *time.Time `json:"stopBy,omitempty"`
}

// Valid checks a renewal response against Native Worker API timing and
// cancellation invariants.
func (renewal Renewal) Valid() bool {
	ttl := renewal.ExpiresAt.Sub(renewal.ServerTime)
	if !ValidLeaseIdentifier(renewal.LeaseID) ||
		!ValidLeaseIdentifier(renewal.AttemptID) || renewal.RunRevision < 1 ||
		renewal.ServerTime.IsZero() || renewal.ExpiresAt.IsZero() ||
		!ValidLeasePolicy(ttl, time.Duration(renewal.RenewAfterSeconds)*time.Second) {
		return false
	}
	if !renewal.CancellationRequested {
		return renewal.StopBy == nil
	}

	return renewal.StopBy != nil && renewal.StopBy.After(renewal.ServerTime) &&
		!renewal.StopBy.After(renewal.ExpiresAt)
}

// RenewalStore is the privileged persistence boundary for native lease
// renewal. Implementations must authenticate the complete authority, serialize
// with Run cancellation and lease replacement, and use an authoritative clock.
type RenewalStore interface {
	// RenewNativeLease conditionally advances the Run phase, extends the current
	// lease and returns cancellation state in one transaction. ErrLeaseGone hides
	// absent, expired and mismatched authority; run.ErrRevision reports a stale
	// Run fence; run.ErrConflict reports phase regression.
	RenewNativeLease(
		ctx context.Context,
		workerID string,
		request RenewalRequest,
		ttl time.Duration,
		cancellationGrace time.Duration,
	) (Renewal, error)
}

// Renewer binds native renewal persistence to server-owned timing policy.
type Renewer struct {
	store             RenewalStore
	ttl               time.Duration
	cancellationGrace time.Duration
}

// NewRenewer constructs a renewal use case with bounded lease and cancellation
// timing.
func NewRenewer(
	store RenewalStore,
	ttl time.Duration,
	cancellationGrace time.Duration,
) (*Renewer, error) {
	if store == nil || !run.ValidLeaseTTL(ttl) ||
		!ValidCancellationGrace(cancellationGrace, ttl) {
		return nil, run.ErrValidation
	}

	return &Renewer{store: store, ttl: ttl, cancellationGrace: cancellationGrace}, nil
}

// Renew validates caller-controlled authority before delegating the atomic
// renewal decision to storage.
func (renewer *Renewer) Renew(
	ctx context.Context,
	workerID string,
	request RenewalRequest,
) (Renewal, error) {
	if renewer == nil || !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return Renewal{}, run.ErrValidation
	}

	return renewer.store.RenewNativeLease(
		ctx,
		workerID,
		request,
		renewer.ttl,
		renewer.cancellationGrace,
	)
}

// AdvancePhase applies the monotonic Native Worker phase mapping to a Run.
// One report may cross internal control-plane states that have no separate
// worker phase, and every crossed state receives its own Run revision.
func AdvancePhase(current run.Run, expectedRevision int64, phase Phase, now time.Time) (run.Run, error) {
	if current.Revision != expectedRevision {
		return run.Run{}, run.ErrRevision
	}
	if !phase.Valid() {
		return run.Run{}, run.ErrValidation
	}
	target, ok := phaseTarget(phase)
	if !ok {
		return run.Run{}, run.ErrValidation
	}
	currentIndex := phaseStateIndex(current.State)
	targetIndex := phaseStateIndex(target)
	if currentIndex < 0 || currentIndex > targetIndex {
		return run.Run{}, run.ErrConflict
	}

	next := current.Clone()
	for index := currentIndex + 1; index <= targetIndex; index++ {
		var err error
		next, err = next.Transition(next.Revision, run.Change{State: nativePhaseStates[index]}, now)
		if err != nil {
			return run.Run{}, err
		}
	}

	return next, nil
}

// ValidCancellationGrace accepts a whole-second positive stop window that does
// not exceed the renewed lease lifetime.
func ValidCancellationGrace(grace time.Duration, ttl time.Duration) bool {
	return grace >= 5*time.Second && grace <= ttl && grace%time.Second == 0
}

var nativePhaseStates = [...]run.State{
	run.StateCreated,
	run.StateValidating,
	run.StateProvisioning,
	run.StateWarmingUp,
	run.StateRunning,
	run.StateCollecting,
}

func phaseTarget(phase Phase) (run.State, bool) {
	switch phase {
	case PhasePreparing:
		return run.StateWarmingUp, true
	case PhaseRunning:
		return run.StateRunning, true
	case PhaseUploading, PhaseFinalizing:
		return run.StateCollecting, true
	default:
		return "", false
	}
}

func phaseStateIndex(state run.State) int {
	for index, candidate := range nativePhaseStates {
		if candidate == state {
			return index
		}
	}

	return -1
}
