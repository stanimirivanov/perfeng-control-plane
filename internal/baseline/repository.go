package baseline

import "context"

// Repository stores principal-scoped baseline versions. Implementations must be
// safe for concurrent use, honor context cancellation, serialize lifecycle
// changes for each version, and return snapshots isolated from stored state.
// Missing and cross-principal records or evidence are indistinguishable.
//
// Persistence failures can return run.ErrUnavailable, and a commit failure can
// have an uncertain outcome. Callers resolve an uncertain creation with
// GetBaseline using the same principal, ID and version. Repository methods do
// not select a baseline, evaluate qualification evidence or authorize actors.
type Repository interface {
	// CreateBaseline creates revision one in CANDIDATE using the store's
	// authoritative clock. The normalized artifact must be registered for a
	// COMPLETED source Run owned by principal and must exactly match input.
	//
	// run.ErrValidation reports malformed input, an unfinished Run or mismatched
	// evidence. run.ErrNotFound reports missing or cross-principal source
	// evidence. run.ErrConflict reports an existing baseline with the same
	// principal, ID and version; existing records are never overwritten.
	CreateBaseline(ctx context.Context, principal string, input Create) (Record, error)

	// GetBaseline returns the exact version visible to principal. ErrNotFound
	// reports a missing or cross-principal record.
	GetBaseline(ctx context.Context, principal, id, version string) (Record, error)

	// TransitionBaseline applies Change to the locked current snapshot using the
	// store's authoritative clock. It returns the same revision, validation and
	// transition errors as Record.Transition. run.ErrNotFound reports a missing
	// or cross-principal record. A successful mutation persists the revision,
	// state and appended lifecycle event atomically.
	TransitionBaseline(
		ctx context.Context,
		principal, id, version string,
		expectedRevision int64,
		change Change,
	) (Record, error)
}
