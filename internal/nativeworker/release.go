package nativeworker

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReleaseReason explains why a worker stopped without ordinary completion.
type ReleaseReason string

const (
	// ReleaseCancellationRequested confirms that requested cancellation stopped
	// the worker's complete browser process tree.
	ReleaseCancellationRequested ReleaseReason = "CANCELLATION_REQUESTED"
	// ReleaseLeaseLost means the worker can no longer prove current authority.
	ReleaseLeaseLost ReleaseReason = "LEASE_LOST"
	// ReleaseWorkerShutdown means the interactive worker agent is stopping.
	ReleaseWorkerShutdown ReleaseReason = "WORKER_SHUTDOWN"
	// ReleasePreparationFailed means preparation stopped before authoritative
	// execution or partial evidence acceptance.
	ReleasePreparationFailed ReleaseReason = "PREPARATION_FAILED"
)

// Valid reports whether reason belongs to the Native Worker API.
func (reason ReleaseReason) Valid() bool {
	switch reason {
	case ReleaseCancellationRequested, ReleaseLeaseLost,
		ReleaseWorkerShutdown, ReleasePreparationFailed:
		return true
	default:
		return false
	}
}

// ReleaseRequest contains the complete authority and revision fence for one
// native lease release. Capability and Message must not enter diagnostics.
type ReleaseRequest struct {
	LeaseID             string
	AttemptID           string
	ExpectedRunRevision int64
	Reason              ReleaseReason
	Message             string
	Capability          string
}

// Valid checks release syntax without establishing current ownership.
func (request ReleaseRequest) Valid() bool {
	return ValidLeaseIdentifier(request.LeaseID) &&
		ValidLeaseIdentifier(request.AttemptID) && request.ExpectedRunRevision >= 1 &&
		request.Reason.Valid() && validReleaseMessage(request.Message) &&
		ValidLeaseCapability(request.Capability)
}

// String identifies a release without exposing its capability or message.
func (request ReleaseRequest) String() string {
	return fmt.Sprintf(
		"native lease release %s attempt %s reason %s",
		request.LeaseID,
		request.AttemptID,
		request.Reason,
	)
}

// GoString identifies a release in diagnostic formatting without exposing its
// capability or message.
func (request ReleaseRequest) GoString() string {
	return request.String()
}

// ReleaseStore is the privileged persistence boundary for native lease
// release. Implementations must serialize with renewal, cancellation and lease
// replacement and make a duplicate of the same attempt and reason idempotent.
type ReleaseStore interface {
	// ReleaseNativeLease expires current authority and records the first release
	// reason. A different reason for an already released attempt returns
	// run.ErrConflict; absent, expired or mismatched authority returns ErrLeaseGone.
	ReleaseNativeLease(context.Context, string, ReleaseRequest) error
}

// Releaser validates release authority before invoking fenced persistence.
type Releaser struct {
	store ReleaseStore
}

// NewReleaser constructs a native lease release use case.
func NewReleaser(store ReleaseStore) (*Releaser, error) {
	if store == nil {
		return nil, run.ErrValidation
	}

	return &Releaser{store: store}, nil
}

// Release validates caller-controlled authority before delegating the atomic
// release decision to storage.
func (releaser *Releaser) Release(
	ctx context.Context,
	workerID string,
	request ReleaseRequest,
) error {
	if releaser == nil || !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return run.ErrValidation
	}

	return releaser.store.ReleaseNativeLease(ctx, workerID, request)
}

func validReleaseMessage(message string) bool {
	return message == "" || utf8.ValidString(message) && utf8.RuneCountInString(message) <= 512
}
