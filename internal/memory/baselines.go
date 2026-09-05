package memory

import (
	"context"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// CreateBaseline verifies completed registered evidence and stores a new candidate.
func (m *Repository) CreateBaseline(
	ctx context.Context,
	principal string,
	input baseline.Create,
) (baseline.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return baseline.Record{}, err
	}
	record, err := baseline.New(input, m.baselineTime())
	if err != nil {
		return baseline.Record{}, err
	}
	source, err := m.visible(principal, record.SourceRunID)
	if err != nil {
		return baseline.Record{}, err
	}
	if source.State != run.StateCompleted {
		return baseline.Record{}, run.ErrValidation
	}
	registered, exists := m.artifacts[record.Artifact.ID]
	if !exists {
		return baseline.Record{}, run.ErrNotFound
	}
	if registered != record.Artifact {
		return baseline.Record{}, run.ErrValidation
	}
	key := baselineKey{principal, record.ID, record.Version}
	if _, exists := m.baselines[key]; exists {
		return baseline.Record{}, run.ErrConflict
	}
	m.baselines[key] = record

	return record.Clone(), nil
}

// GetBaseline returns the exact principal-owned baseline version.
func (m *Repository) GetBaseline(
	ctx context.Context,
	principal, id, version string,
) (baseline.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return baseline.Record{}, err
	}
	record, exists := m.baselines[baselineKey{principal, id, version}]
	if !exists || principal == "" {
		return baseline.Record{}, run.ErrNotFound
	}

	return record.Clone(), nil
}

// ResolveApprovedBaseline returns an exact approved compatible version.
func (m *Repository) ResolveApprovedBaseline(
	ctx context.Context,
	principal string,
	selection baseline.Selection,
) (baseline.Record, bool, error) {
	if principal == "" || selection.Validate() != nil {
		return baseline.Record{}, false, run.ErrValidation
	}
	record, err := m.GetBaseline(ctx, principal, selection.ID, selection.Version)
	if errors.Is(err, run.ErrNotFound) {
		return baseline.Record{}, false, nil
	}
	if err != nil {
		return baseline.Record{}, false, err
	}
	if !record.MatchesSelection(selection) {
		return baseline.Record{}, false, nil
	}

	return record, true, nil
}

// TransitionBaseline serializes a revision-checked lifecycle decision.
func (m *Repository) TransitionBaseline(
	ctx context.Context,
	principal, id, version string,
	expectedRevision int64,
	change baseline.Change,
) (baseline.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return baseline.Record{}, err
	}
	key := baselineKey{principal, id, version}
	current, exists := m.baselines[key]
	if !exists || principal == "" {
		return baseline.Record{}, run.ErrNotFound
	}
	next, err := current.Transition(expectedRevision, change, m.baselineTime())
	if err != nil {
		return baseline.Record{}, err
	}
	m.baselines[key] = next

	return next.Clone(), nil
}

func (m *Repository) baselineTime() time.Time {
	return m.now().UTC().Truncate(time.Microsecond)
}
