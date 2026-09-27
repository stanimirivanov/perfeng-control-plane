package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrUploadGrantMismatch means artifact registration does not match the
// immutable upload declaration reserved for the current attempt.
var ErrUploadGrantMismatch = errors.New("artifact registration does not match upload grant")

// ErrArtifactVerification means the selected uploaded object is absent or its
// metadata or bytes do not match the immutable upload declaration.
var ErrArtifactVerification = errors.New("uploaded artifact does not match its declaration")

// ArtifactRegistrationRequest contains the complete authority and immutable
// upload facts supplied when a native worker registers one uploaded artifact.
// Capability must never be logged or persisted.
type ArtifactRegistrationRequest struct {
	LeaseID             string
	AttemptID           string
	ExpectedRunRevision int64
	GrantID             string
	Slot                string
	Size                int64
	SHA256              string
	Capability          string
}

// Valid checks registration syntax without establishing current lease or
// upload-grant authority.
func (request ArtifactRegistrationRequest) Valid() bool {
	return ValidLeaseIdentifier(request.LeaseID) &&
		ValidLeaseIdentifier(request.AttemptID) && request.ExpectedRunRevision >= 1 &&
		ValidLeaseIdentifier(request.GrantID) && rawresult.ValidResourceID(request.Slot) &&
		request.Size >= 1 && request.Size <= maximumArtifactBytes &&
		uploadSHA256Pattern.MatchString(request.SHA256) &&
		ValidLeaseCapability(request.Capability)
}

// String identifies a registration request without exposing its lease
// capability or checksum.
func (request ArtifactRegistrationRequest) String() string {
	return fmt.Sprintf(
		"native artifact registration %s attempt %s slot %s",
		request.LeaseID,
		request.AttemptID,
		request.Slot,
	)
}

// GoString identifies a registration request in diagnostic formatting without
// exposing its lease capability or checksum.
func (request ArtifactRegistrationRequest) GoString() string {
	return request.String()
}

// ArtifactCandidate combines one durable upload reservation with the reviewed
// artifact semantics needed to verify and register it.
type ArtifactCandidate struct {
	Reservation UploadReservation
	Kind        string
	Format      string
}

// Valid checks the complete immutable candidate selected from durable state.
func (candidate ArtifactCandidate) Valid() bool {
	return candidate.Reservation.Valid() && rawresult.ValidResourceID(candidate.Kind) &&
		rawresult.ValidResourceID(candidate.Format)
}

// VerifiedArtifact records the approved object location after exact bytes and
// metadata have been verified outside a database transaction.
type VerifiedArtifact struct {
	Candidate ArtifactCandidate
	URI       string
}

// Valid checks that the verified location is the candidate's canonical S3
// object and contains no endpoint, credential or query material.
func (artifact VerifiedArtifact) Valid() bool {
	return artifact.Candidate.Valid() &&
		validNativeArtifactURI(artifact.URI, artifact.Candidate.Reservation.ObjectKey)
}

// ArtifactReference is the immutable Native Worker API v1 response and durable
// evidence identity. Format is retained internally for later manifest handling.
type ArtifactReference struct {
	ArtifactID   string    `json:"artifactId"`
	RunID        string    `json:"runId"`
	AttemptID    string    `json:"attemptId"`
	Slot         string    `json:"slot"`
	Kind         string    `json:"kind"`
	URI          string    `json:"uri"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	MediaType    string    `json:"mediaType"`
	RegisteredAt time.Time `json:"registeredAt"`
	Format       string    `json:"-"`
}

// Valid checks the complete accepted reference and its canonical attempt key.
func (reference ArtifactReference) Valid() bool {
	key := fmt.Sprintf(
		"runs/%s/attempts/%s/%s",
		reference.RunID,
		reference.AttemptID,
		reference.Slot,
	)

	return ValidLeaseIdentifier(reference.ArtifactID) && contract.ValidID(reference.RunID) &&
		ValidLeaseIdentifier(reference.AttemptID) && rawresult.ValidResourceID(reference.Slot) &&
		rawresult.ValidResourceID(reference.Kind) && rawresult.ValidResourceID(reference.Format) &&
		validNativeArtifactURI(reference.URI, key) && reference.Size >= 1 &&
		reference.Size <= maximumArtifactBytes && uploadSHA256Pattern.MatchString(reference.SHA256) &&
		mediaTypePattern.MatchString(reference.MediaType) && !reference.RegisteredAt.IsZero() &&
		reference.RegisteredAt.Location() == time.UTC
}

// ArtifactRegistrationStore is the privileged persistence boundary for native
// artifact acceptance. Implementations must recheck current lease, attempt,
// revision and grant authority both before verification and at final commit.
type ArtifactRegistrationStore interface {
	// PrepareNativeArtifact returns the immutable candidate selected under current
	// authority. It must not call object storage while holding database locks.
	PrepareNativeArtifact(
		ctx context.Context,
		workerID string,
		request ArtifactRegistrationRequest,
	) (ArtifactCandidate, error)

	// RegisterNativeArtifact rechecks current authority and atomically preserves
	// one already verified reference. An identical retry returns the first
	// reference; changed verification returns ErrUploadGrantMismatch.
	RegisterNativeArtifact(
		ctx context.Context,
		workerID string,
		request ArtifactRegistrationRequest,
		verified VerifiedArtifact,
	) (ArtifactReference, error)
}

// ArtifactVerifier verifies the exact object selected by a durable candidate
// without changing control-plane state.
type ArtifactVerifier interface {
	// VerifyNativeArtifact returns the canonical object location only after its
	// response metadata, byte count and SHA-256 match the reservation. An absent
	// or mismatched object returns ErrArtifactVerification; dependency failures
	// preserve run.ErrUnavailable or context error identity.
	VerifyNativeArtifact(context.Context, ArtifactCandidate) (VerifiedArtifact, error)
}

// ArtifactRegistrar coordinates object verification outside the final fenced
// persistence transaction.
type ArtifactRegistrar struct {
	store    ArtifactRegistrationStore
	verifier ArtifactVerifier
}

// NewArtifactRegistrar constructs the native artifact-registration use case.
func NewArtifactRegistrar(
	store ArtifactRegistrationStore,
	verifier ArtifactVerifier,
) (*ArtifactRegistrar, error) {
	if store == nil || verifier == nil {
		return nil, run.ErrValidation
	}

	return &ArtifactRegistrar{store: store, verifier: verifier}, nil
}

// Register verifies one uploaded object and accepts it only if lease and grant
// authority remain current after verification completes.
func (registrar *ArtifactRegistrar) Register(
	ctx context.Context,
	workerID string,
	request ArtifactRegistrationRequest,
) (ArtifactReference, error) {
	if registrar == nil || registrar.store == nil || registrar.verifier == nil ||
		!contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return ArtifactReference{}, run.ErrValidation
	}
	candidate, err := registrar.store.PrepareNativeArtifact(ctx, workerID, request)
	if err != nil {
		return ArtifactReference{}, err
	}
	if !candidate.Valid() {
		return ArtifactReference{}, errors.New("invalid native artifact candidate")
	}
	verified, err := registrar.verifier.VerifyNativeArtifact(ctx, candidate)
	if err != nil {
		return ArtifactReference{}, err
	}
	if !verified.Valid() || verified.Candidate != candidate {
		return ArtifactReference{}, errors.New("invalid verified native artifact")
	}

	return registrar.store.RegisterNativeArtifact(ctx, workerID, request, verified)
}

func validNativeArtifactURI(value string, objectKey string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "s3" && parsed.Host != "" &&
		parsed.Host == parsed.Hostname() && parsed.User == nil && parsed.RawPath == "" &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" &&
		strings.TrimPrefix(parsed.Path, "/") == objectKey
}
