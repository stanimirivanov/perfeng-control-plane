package nativeworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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

// UploadHeaders is the closed set of headers covered by a native artifact PUT
// signature. Values are sensitive delivery material and must not be logged or
// persisted.
type UploadHeaders struct {
	ContentType    string `json:"content-type"`
	ChecksumSHA256 string `json:"x-amz-checksum-sha256"`
}

// Valid checks that the signed headers reproduce the reserved media type and
// full-object SHA-256 digest.
func (headers UploadHeaders) Valid(reservation UploadReservation) bool {
	return validUploadHeaders(headers, reservation.MediaType, reservation.SHA256)
}

func validUploadHeaders(headers UploadHeaders, mediaType string, sha256 string) bool {
	checksum, err := base64.StdEncoding.DecodeString(headers.ChecksumSHA256)
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(sha256)
	if err != nil {
		return false
	}

	return headers.ContentType == mediaType && len(checksum) == 32 &&
		bytes.Equal(checksum, expected)
}

// String redacts signed header values from diagnostic formatting.
func (headers UploadHeaders) String() string {
	return "native upload headers [redacted]"
}

// GoString redacts signed header values from Go diagnostic formatting.
func (headers UploadHeaders) GoString() string {
	return headers.String()
}

// UploadAuthorization is short-lived delivery material generated for one
// durable upload reservation. URL and Headers must never be logged or
// persisted.
type UploadAuthorization struct {
	URL       string
	Headers   UploadHeaders
	Method    string
	ExpiresAt time.Time
}

// Valid checks the URL shape, signed-header values and lease-bounded expiry
// against the declared fields of reservation.
func (authorization UploadAuthorization) Valid(reservation UploadReservation) bool {
	return reservation.Valid() && validPresignedUploadURL(authorization.URL) &&
		authorization.Headers.Valid(reservation) && authorization.Method == "PUT" &&
		!authorization.ExpiresAt.IsZero() && authorization.ExpiresAt.Location() == time.UTC &&
		authorization.ExpiresAt.After(reservation.IssuedAt) &&
		!authorization.ExpiresAt.After(reservation.ExpiresAt)
}

// String redacts the presigned URL and headers from diagnostic formatting.
func (authorization UploadAuthorization) String() string {
	return fmt.Sprintf("native upload authorization %s expiring %s", authorization.Method, authorization.ExpiresAt)
}

// GoString redacts the presigned URL and headers from Go diagnostic formatting.
func (authorization UploadAuthorization) GoString() string {
	return authorization.String()
}

// UploadGrant is the Native Worker API response shape produced from one durable
// reservation and its ephemeral upload authorization in either generation.
type UploadGrant struct {
	GrantID       string        `json:"grantId"`
	LeaseID       string        `json:"leaseId"`
	AttemptID     string        `json:"attemptId"`
	RunID         string        `json:"runId"`
	Slot          string        `json:"slot"`
	ObjectKey     string        `json:"objectKey"`
	UploadURL     string        `json:"uploadUrl"`
	UploadHeaders UploadHeaders `json:"uploadHeaders"`
	Method        string        `json:"method"`
	ExpiresAt     time.Time     `json:"expiresAt"`
	MaximumBytes  int64         `json:"maximumBytes"`
	SHA256        string        `json:"sha256"`
}

// Valid checks the complete Native Worker API upload-grant response.
func (grant UploadGrant) Valid() bool {
	expectedKey := fmt.Sprintf(
		"runs/%s/attempts/%s/%s",
		grant.RunID,
		grant.AttemptID,
		grant.Slot,
	)

	return ValidLeaseIdentifier(grant.GrantID) && ValidLeaseIdentifier(grant.LeaseID) &&
		ValidLeaseIdentifier(grant.AttemptID) && contract.ValidID(grant.RunID) &&
		rawresult.ValidResourceID(grant.Slot) && grant.ObjectKey == expectedKey &&
		validPresignedUploadURL(grant.UploadURL) && grant.Method == "PUT" &&
		!grant.ExpiresAt.IsZero() && grant.ExpiresAt.Location() == time.UTC &&
		grant.MaximumBytes >= 1 && grant.MaximumBytes <= maximumArtifactBytes &&
		uploadSHA256Pattern.MatchString(grant.SHA256) &&
		mediaTypePattern.MatchString(grant.UploadHeaders.ContentType) &&
		validUploadHeaders(grant.UploadHeaders, grant.UploadHeaders.ContentType, grant.SHA256)
}

// String identifies a grant without exposing its presigned URL or headers.
func (grant UploadGrant) String() string {
	return fmt.Sprintf("native upload grant %s for run %s slot %s", grant.GrantID, grant.RunID, grant.Slot)
}

// GoString identifies a grant in diagnostic formatting without exposing its
// presigned URL or headers.
func (grant UploadGrant) GoString() string {
	return grant.String()
}

// UploadSigner creates ephemeral delivery material for one committed upload
// reservation without changing durable state.
type UploadSigner interface {
	// PresignNativeUpload authorizes only the reservation's exact PUT request and
	// returns an expiry no later than the durable reservation.
	PresignNativeUpload(context.Context, UploadReservation) (UploadAuthorization, error)
}

// UploadGrantIssuer commits a fenced reservation before asking the signer for
// ephemeral delivery material outside the database transaction.
type UploadGrantIssuer struct {
	reserver *UploadGrantReserver
	signer   UploadSigner
}

// NewUploadGrantIssuer constructs the complete upload-grant use case.
func NewUploadGrantIssuer(
	store UploadGrantStore,
	signer UploadSigner,
	ttl time.Duration,
) (*UploadGrantIssuer, error) {
	reserver, err := NewUploadGrantReserver(store, ttl)
	if err != nil || signer == nil {
		return nil, run.ErrValidation
	}

	return &UploadGrantIssuer{reserver: reserver, signer: signer}, nil
}

// Issue reserves the declared object before creating short-lived delivery
// material. A signing failure leaves the non-secret reservation available for
// an identical retry.
func (issuer *UploadGrantIssuer) Issue(
	ctx context.Context,
	workerID string,
	request UploadRequest,
) (UploadGrant, error) {
	if issuer == nil || issuer.reserver == nil || issuer.signer == nil {
		return UploadGrant{}, run.ErrValidation
	}
	reservation, err := issuer.reserver.Reserve(ctx, workerID, request)
	if err != nil {
		return UploadGrant{}, err
	}
	if !reservation.Valid() {
		return UploadGrant{}, errors.New("invalid native upload reservation")
	}
	authorization, err := issuer.signer.PresignNativeUpload(ctx, reservation)
	if err != nil {
		return UploadGrant{}, err
	}
	if !authorization.Valid(reservation) {
		return UploadGrant{}, errors.New("invalid native upload authorization")
	}
	grant := UploadGrant{
		GrantID: reservation.GrantID, LeaseID: reservation.LeaseID,
		AttemptID: reservation.AttemptID, RunID: reservation.RunID,
		Slot: reservation.Slot, ObjectKey: reservation.ObjectKey,
		UploadURL: authorization.URL, UploadHeaders: authorization.Headers,
		Method: authorization.Method, ExpiresAt: authorization.ExpiresAt,
		MaximumBytes: reservation.MaximumBytes, SHA256: reservation.SHA256,
	}
	if !grant.Valid() {
		return UploadGrant{}, errors.New("invalid native upload grant")
	}

	return grant, nil
}

func validPresignedUploadURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Path != "" && parsed.RawQuery != "" && parsed.Fragment == ""
}
