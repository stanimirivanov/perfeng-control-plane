package memory

import (
	"context"
	"sort"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ run.ArtifactRepository = (*Repository)(nil)

// RegisterArtifact stores one immutable reference for an owned Run.
func (m *Repository) RegisterArtifact(
	ctx context.Context,
	principal string,
	artifact run.Artifact,
) error {
	artifact.ID = strings.ToLower(artifact.ID)
	if artifact.Validate() != nil {
		return run.ErrValidation
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := m.visible(principal, artifact.RunID); err != nil {
		return err
	}
	if previous, exists := m.artifacts[artifact.ID]; exists {
		if previous != artifact {
			return run.ErrArtifactConflict
		}

		return nil
	}
	m.artifacts[artifact.ID] = artifact

	return nil
}

// GetArtifact returns one immutable reference visible to the principal.
func (m *Repository) GetArtifact(
	ctx context.Context,
	principal string,
	runID string,
	id string,
) (run.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if _, err := m.visible(principal, runID); err != nil {
		return run.Artifact{}, err
	}
	artifact, exists := m.artifacts[strings.ToLower(id)]
	if !exists || artifact.RunID != runID {
		return run.Artifact{}, run.ErrNotFound
	}

	return artifact, nil
}

// ListArtifacts returns an owned Run's references in artifact-ID order.
func (m *Repository) ListArtifacts(
	ctx context.Context,
	principal string,
	runID string,
) ([]run.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := m.visible(principal, runID); err != nil {
		return nil, err
	}

	artifacts := make([]run.Artifact, 0)
	for _, artifact := range m.artifacts {
		if artifact.RunID == runID {
			artifacts = append(artifacts, artifact)
		}
	}
	sort.Slice(artifacts, func(left, right int) bool {
		return artifacts[left].ID < artifacts[right].ID
	})

	return artifacts, nil
}
