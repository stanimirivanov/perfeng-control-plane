package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumUploadGrantTTL = 5 * time.Minute

var uploadSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ErrArtifactPlan means an upload declaration does not match the artifact slot
// assigned to the current native attempt.
var ErrArtifactPlan = errors.New("artifact does not match the assigned plan")

// UploadRequest contains the complete authority, revision fence and declared
// object facts for one native artifact upload grant. Capability must never be
// logged or persisted.
type UploadRequest struct {
	LeaseID             string
	AttemptID           string
	ExpectedRunRevision int64
	Slot                string
	Size                int64
	SHA256              string
	MediaType           string
	Capability          string
}

// Valid checks upload syntax without establishing current lease ownership or
// matching the declaration to the assigned artifact plan.
func (request UploadRequest) Valid() bool {
	return ValidLeaseIdentifier(request.LeaseID) &&
		ValidLeaseIdentifier(request.AttemptID) && request.ExpectedRunRevision >= 1 &&
		rawresult.ValidResourceID(request.Slot) &&
		request.Size >= 1 && request.Size <= maximumArtifactBytes &&
		uploadSHA256Pattern.MatchString(request.SHA256) &&
		mediaTypePattern.MatchString(request.MediaType) &&
		ValidLeaseCapability(request.Capability)
}

// String identifies an upload request without exposing its lease capability or
// checksum.
func (request UploadRequest) String() string {
	return fmt.Sprintf(
		"native upload request %s attempt %s slot %s",
		request.LeaseID,
		request.AttemptID,
		request.Slot,
	)
}

// GoString identifies an upload request in diagnostic formatting without
// exposing its lease capability or checksum.
func (request UploadRequest) GoString() string {
	return request.String()
}

// UploadReservation is the durable non-secret identity and declared object
// metadata from which a short-lived presigned PUT can be generated.
type UploadReservation struct {
	GrantID      string
	LeaseID      string
	AttemptID    string
	RunID        string
	Slot         string
	ObjectKey    string
	Size         int64
	MaximumBytes int64
	SHA256       string
	MediaType    string
	IssuedAt     time.Time
	ExpiresAt    time.Time
}

// Valid checks the complete durable reservation and canonical object identity.
func (reservation UploadReservation) Valid() bool {
	expectedKey := fmt.Sprintf(
		"runs/%s/attempts/%s/%s",
		reservation.RunID,
		reservation.AttemptID,
		reservation.Slot,
	)
	lifetime := reservation.ExpiresAt.Sub(reservation.IssuedAt)

	return ValidLeaseIdentifier(reservation.GrantID) &&
		ValidLeaseIdentifier(reservation.LeaseID) &&
		ValidLeaseIdentifier(reservation.AttemptID) &&
		contract.ValidID(reservation.RunID) && rawresult.ValidResourceID(reservation.Slot) &&
		reservation.ObjectKey == expectedKey && reservation.Size >= 1 &&
		reservation.MaximumBytes >= reservation.Size &&
		reservation.MaximumBytes <= maximumArtifactBytes &&
		uploadSHA256Pattern.MatchString(reservation.SHA256) &&
		mediaTypePattern.MatchString(reservation.MediaType) &&
		!reservation.IssuedAt.IsZero() && reservation.IssuedAt.Location() == time.UTC &&
		!reservation.ExpiresAt.IsZero() && reservation.ExpiresAt.Location() == time.UTC &&
		lifetime > 0 && lifetime <= maximumUploadGrantTTL
}

// UploadGrantStore is the privileged persistence boundary for native upload
// reservations. Implementations must serialize current lease authority and use
// an authoritative clock without performing object-store calls in the
// transaction.
type UploadGrantStore interface {
	// ReserveNativeUploadGrant creates or refreshes the non-secret reservation
	// for one attempt and assigned slot. An identical retry retains the grant
	// identity; a changed declaration returns run.ErrConflict. ErrLeaseGone hides
	// absent, expired, released, terminal or mismatched authority; run.ErrRevision
	// reports a stale Run fence; ErrArtifactPlan reports a declaration outside the
	// assigned plan.
	ReserveNativeUploadGrant(
		ctx context.Context,
		workerID string,
		request UploadRequest,
		ttl time.Duration,
	) (UploadReservation, error)
}

// UploadGrantReserver binds durable reservation to a server-owned lifetime.
type UploadGrantReserver struct {
	store UploadGrantStore
	ttl   time.Duration
}

// NewUploadGrantReserver constructs an upload reservation use case with a
// contract-compatible whole-second lifetime.
func NewUploadGrantReserver(
	store UploadGrantStore,
	ttl time.Duration,
) (*UploadGrantReserver, error) {
	if store == nil || !ValidUploadGrantTTL(ttl) {
		return nil, run.ErrValidation
	}

	return &UploadGrantReserver{store: store, ttl: ttl}, nil
}

// Reserve validates caller-controlled authority and declaration syntax before
// delegating the atomic reservation decision to storage.
func (reserver *UploadGrantReserver) Reserve(
	ctx context.Context,
	workerID string,
	request UploadRequest,
) (UploadReservation, error) {
	if reserver == nil || !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return UploadReservation{}, run.ErrValidation
	}

	return reserver.store.ReserveNativeUploadGrant(ctx, workerID, request, reserver.ttl)
}

// ValidUploadGrantTTL accepts a whole-second positive lifetime no longer than
// five minutes.
func ValidUploadGrantTTL(value time.Duration) bool {
	return value >= time.Second && value <= maximumUploadGrantTTL && value%time.Second == 0
}
