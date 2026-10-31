package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ nativeworker.BaselineLeaseStore = (*Repository)(nil)

// EligibleNativeWorkerV2 returns one fresh, qualified and authorized v2
// observation. The HTTP composition keeps authorization off unless explicitly enabled.
func (r *Repository) EligibleNativeWorkerV2(
	ctx context.Context,
	workerID string,
	maximumAge time.Duration,
) (nativeworker.PresenceObservation, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) ||
		!nativeworker.ValidPresenceMaximumAge(maximumAge) {
		return nativeworker.PresenceObservation{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	var observedAt time.Time
	var document []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT observed_at,presence
		FROM perfeng_control.native_worker_presence_v2
		WHERE worker_id=$1
		AND availability='AVAILABLE'
		AND qualified
		AND authorized_to_acquire
		AND observed_at > clock_timestamp() - make_interval(secs => $2)
	`, workerID, int(maximumAge/time.Second)).Scan(&observedAt, &document)
	if err != nil {
		return nativeworker.PresenceObservation{}, storageError(err)
	}
	presence, err := nativeworker.ParseV2Presence(document, nativeworker.MaximumPresenceBytes)
	if err != nil || presence.Availability != "AVAILABLE" {
		return nativeworker.PresenceObservation{}, errors.New("invalid stored v2 native worker presence")
	}

	return nativeworker.PresenceObservation{
		WorkerID: workerID, ObservedAt: observedAt.UTC(), Presence: presence,
	}, nil
}

// AcquireNativeBaselineLease fences one v2 attempt by locking the worker before
// the Run and reading database time after both locks. It never trusts the
// advisory selection alone.
func (r *Repository) AcquireNativeBaselineLease(
	ctx context.Context,
	workerID string,
	expectedPresenceAt time.Time,
	candidate nativeworker.Candidate,
	plan nativeworker.BaselinePlan,
	presenceMaximumAge time.Duration,
	ttl time.Duration,
	renewAfter time.Duration,
) (nativeworker.BaselineLease, bool, error) {
	if !validNativeBaselineLeaseRequest(
		workerID, expectedPresenceAt, candidate, plan, presenceMaximumAge, ttl, renewAfter,
	) {
		return nativeworker.BaselineLease{}, false, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	defer tx.Rollback()

	if err := lockNativeWorkerCapacity(ctx, tx, workerID); err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	observedAt, presence, eligible, err := lockNativeWorkerV2Presence(ctx, tx, workerID)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	if !eligible || !observedAt.Equal(expectedPresenceAt) || !presence.SupportsBaseline(plan) {
		return nativeworker.BaselineLease{}, false, nil
	}
	current, target, principal, err := lockNativeRun(ctx, tx, candidate.Run.ID)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	if now.Sub(observedAt) >= presenceMaximumAge || principal != candidate.Principal ||
		target != run.ExecutionTargetNativeLinux || current.State != run.StateCreated ||
		current.Revision != candidate.Run.Revision || current.Request != candidate.Run.Request ||
		!baselinePlanMatchesRun(plan, current) {
		return nativeworker.BaselineLease{}, false, nil
	}
	occupied, err := nativeWorkerOccupied(ctx, tx, workerID, now)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	if occupied {
		return nativeworker.BaselineLease{}, false, nil
	}
	active, err := nativeRunLeased(ctx, tx, current.ID, now)
	if err != nil {
		return nativeworker.BaselineLease{}, false, err
	}
	if active {
		return nativeworker.BaselineLease{}, false, nil
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return nativeworker.BaselineLease{}, false, run.ErrValidation
	}
	authority, written, err := persistNativeLease(
		ctx, tx, workerID, current.ID, planBytes, now, ttl, renewAfter,
	)
	if err != nil || !written {
		return nativeworker.BaselineLease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return nativeworker.BaselineLease{}, false, storageError(err)
	}

	return nativeworker.BaselineLease{
		LeaseID: authority.leaseID, AttemptID: authority.attemptID, RunID: current.ID,
		RunRevision: current.Revision, IssuedAt: authority.issuedAt,
		ExpiresAt: authority.expiresAt, RenewAfterSeconds: int(renewAfter / time.Second),
		BaselinePlan: plan, Capability: authority.capability,
	}, true, nil
}

func validNativeBaselineLeaseRequest(
	workerID string,
	expectedPresenceAt time.Time,
	candidate nativeworker.Candidate,
	plan nativeworker.BaselinePlan,
	presenceMaximumAge time.Duration,
	ttl time.Duration,
	renewAfter time.Duration,
) bool {
	return contract.ValidNativeWorkerIdentifier(workerID) && !expectedPresenceAt.IsZero() &&
		expectedPresenceAt.Location() == time.UTC && candidate.Principal != "" &&
		contract.ValidID(candidate.Run.ID) && candidate.Run.Revision >= 1 &&
		candidate.Run.State == run.StateCreated && candidate.Run.Request.Validate() == nil &&
		plan.Validate() == nil && nativeworker.ValidPresenceMaximumAge(presenceMaximumAge) &&
		nativeworker.ValidLeasePolicy(ttl, renewAfter)
}

func lockNativeWorkerV2Presence(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
) (time.Time, nativeworker.Presence, bool, error) {
	var observedAt time.Time
	var availability string
	var qualified bool
	var authorized bool
	var document []byte
	err := tx.QueryRowContext(ctx, `
		SELECT observed_at,availability,qualified,authorized_to_acquire,presence
		FROM perfeng_control.native_worker_presence_v2
		WHERE worker_id=$1
		FOR UPDATE
	`, workerID).Scan(&observedAt, &availability, &qualified, &authorized, &document)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nativeworker.Presence{}, false, nil
	}
	if err != nil {
		return time.Time{}, nativeworker.Presence{}, false, storageError(err)
	}
	presence, err := nativeworker.ParseV2Presence(document, nativeworker.MaximumPresenceBytes)
	if err != nil || presence.Availability != availability {
		return time.Time{}, nativeworker.Presence{}, false,
			errors.New("invalid stored v2 native worker presence")
	}

	return observedAt.UTC(), presence,
		availability == "AVAILABLE" && qualified && authorized, nil
}

func baselinePlanMatchesRun(plan nativeworker.BaselinePlan, current run.Run) bool {
	return plan.Assignment.CatalogueTestID == current.Request.TestSuite &&
		plan.Assignment.Profile == current.Request.Profile &&
		plan.Assignment.Target.EnvironmentID == current.Request.Environment.ID
}
