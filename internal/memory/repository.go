// Package memory is a process-local test adapter, never durable storage.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type ownedRun struct {
	principal string
	run       run.Run
}
type bindingKey struct{ principal, key string }

// Repository is a concurrency-safe process-local implementation of run.Repository.
// It is intended only for bounded tests and development because it is not durable.
type Repository struct {
	mu        sync.Mutex
	now       func() time.Time
	runs      map[string]ownedRun
	bindings  map[bindingKey]run.Accepted
	artifacts map[string]run.Artifact
}

var _ run.Repository = (*Repository)(nil)

// New accepts an injectable clock. The adapter retains runs for its entire
// lifetime and loses all state on restart; use only bounded development/tests.
func New(now func() time.Time) *Repository {
	if now == nil {
		now = time.Now
	}

	return &Repository{
		now:       now,
		runs:      make(map[string]ownedRun),
		bindings:  make(map[bindingKey]run.Accepted),
		artifacts: make(map[string]run.Artifact),
	}
}

// Accept serializes all in-memory creates and preserves the original acceptance
// snapshot for a live idempotency binding.
func (m *Repository) Accept(ctx context.Context, principal, key string, request run.Request) (run.Accepted, error) {
	if principal == "" || !run.ValidKey(key) || request.Validate() != nil {
		return run.Accepted{}, run.ErrValidation
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.Accepted{}, err
	}
	now := m.now().UTC()
	scope := bindingKey{principal, key}
	if accepted, ok := m.bindings[scope]; ok && now.Before(accepted.ExpiresAt) {
		if accepted.Run.Request != request {
			return run.Accepted{}, run.ErrConflict
		}
		accepted.Run = accepted.Run.Clone()

		return accepted, nil
	}
	var id string
	for {
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return run.Accepted{}, err
		}
		id = "perf-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:])
		if _, exists := m.runs[id]; !exists {
			break
		}
	}
	created := run.Run{ID: id, State: run.StateCreated, Revision: 1, Request: request, CreatedAt: now, UpdatedAt: now}
	accepted := run.Accepted{Run: created, ExpiresAt: now.Add(24 * time.Hour)}
	m.runs[id] = ownedRun{principal, created}
	m.bindings[scope] = accepted

	return accepted, nil
}

func (m *Repository) visible(principal, id string) (run.Run, error) {
	record, ok := m.runs[id]
	if !ok || principal == "" || record.principal != principal {
		return run.Run{}, run.ErrNotFound
	}

	return record.run, nil
}

// Get returns an isolated snapshot while hiding Runs owned by other principals.
func (m *Repository) Get(ctx context.Context, principal, id string) (run.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	r, err := m.visible(principal, id)

	return r.Clone(), err
}

// Cancel serializes cancellation with worker transitions under the repository lock.
func (m *Repository) Cancel(ctx context.Context, principal, id string) (run.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	r, err := m.visible(principal, id)
	if err != nil {
		return run.Run{}, err
	}
	if r.State == run.StateCancelling || r.State == run.StateAborted {
		return r.Clone(), nil
	}
	if contract.Terminal(string(r.State)) {
		return run.Run{}, run.ErrTerminal
	}
	next, err := r.Transition(r.Revision, run.Change{State: run.StateCancelling}, m.now())
	if err != nil {
		return run.Run{}, err
	}
	m.runs[id] = ownedRun{principal, next}

	return next.Clone(), nil
}

// Advance applies a worker transition under the same lock used by Accept and Cancel.
func (m *Repository) Advance(ctx context.Context, principal, id string, revision int64, change run.Change) (run.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return run.Run{}, err
	}
	r, err := m.visible(principal, id)
	if err != nil {
		return run.Run{}, err
	}
	next, err := r.Transition(revision, change, m.now())
	if err != nil {
		return run.Run{}, err
	}
	m.runs[id] = ownedRun{principal, next}

	return next.Clone(), nil
}
