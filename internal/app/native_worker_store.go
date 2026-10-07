package app

import (
	"context"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
)

// nativeWorkerV2Store routes protocol-neutral lifecycle use cases to the v2
// persistence operations. It cannot fall back to another lease protocol.
type nativeWorkerV2Store struct {
	NativeWorkerStore
}

func (store nativeWorkerV2Store) RenewNativeLease(
	ctx context.Context, workerID string, request nativeworker.RenewalRequest,
	ttl time.Duration, grace time.Duration,
) (nativeworker.Renewal, error) {
	return store.RenewNativeBaselineLease(ctx, workerID, request, ttl, grace)
}

func (store nativeWorkerV2Store) ReleaseNativeLease(
	ctx context.Context, workerID string, request nativeworker.ReleaseRequest,
) error {
	return store.ReleaseNativeBaselineLease(ctx, workerID, request)
}

func (store nativeWorkerV2Store) ReserveNativeUploadGrant(
	ctx context.Context, workerID string, request nativeworker.UploadRequest,
	ttl time.Duration,
) (nativeworker.UploadReservation, error) {
	return store.ReserveNativeBaselineUploadGrant(ctx, workerID, request, ttl)
}

func (store nativeWorkerV2Store) PrepareNativeArtifact(
	ctx context.Context, workerID string, request nativeworker.ArtifactRegistrationRequest,
) (nativeworker.ArtifactCandidate, error) {
	return store.PrepareNativeBaselineArtifact(ctx, workerID, request)
}

func (store nativeWorkerV2Store) RegisterNativeArtifact(
	ctx context.Context, workerID string, request nativeworker.ArtifactRegistrationRequest,
	verified nativeworker.VerifiedArtifact,
) (nativeworker.ArtifactReference, error) {
	return store.RegisterNativeBaselineArtifact(ctx, workerID, request, verified)
}

func (store nativeWorkerV2Store) ReportNativeTerminal(
	ctx context.Context, workerID string, request nativeworker.TerminalRequest,
) (nativeworker.TerminalAcknowledgement, error) {
	return store.ReportNativeBaselineTerminal(ctx, workerID, request)
}
