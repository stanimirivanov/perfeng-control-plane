package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var ctx = context.Background()

const key = "request-key-00000001"

func request(t *testing.T) run.Request {
	t.Helper()
	b, err := contract.Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}
	var r run.Request
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func create(t *testing.T, m *Repository) run.Accepted {
	t.Helper()
	a, err := m.Accept(ctx, "alice", key, request(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func advance(t *testing.T, m *Repository, r run.Run, state string) run.Run {
	t.Helper()
	next, err := m.Advance(ctx, "alice", r.ID, r.Revision, run.Change{State: state})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestReplayScopeExpiryAndIsolation(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	m := New(func() time.Time { return now })
	a := create(t, m)
	if !contract.ValidID(a.Run.ID) || a.ExpiresAt.Sub(a.Run.CreatedAt) != 24*time.Hour {
		t.Fatal("invalid acceptance")
	}
	current := advance(t, m, a.Run, "VALIDATING")
	replay := create(t, m)
	if replay.Run != a.Run || replay.ExpiresAt != a.ExpiresAt {
		t.Fatal("replay not original")
	}
	got, err := m.Get(ctx, "alice", a.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != current.State {
		t.Fatal("replay reset run")
	}
	changed := request(t)
	changed.Profile = "soak"
	if _, err := m.Accept(ctx, "alice", key, changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal(err)
	}
	other, err := m.Accept(ctx, "bob", key, request(t))
	if err != nil || other.Run.ID == a.Run.ID {
		t.Fatal("principal scope collision")
	}
	if _, err := m.Get(ctx, "bob", a.Run.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal read")
	}
	if _, err := m.Cancel(ctx, "bob", a.Run.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal cancel")
	}
	now = now.Add(24 * time.Hour)
	fresh := create(t, m)
	if fresh.Run.ID == a.Run.ID {
		t.Fatal("expired key did not create new run")
	}
	if _, err := m.Get(ctx, "alice", a.Run.ID); err != nil {
		t.Fatal("expiration deleted run")
	}
	if _, err := New(nil).Get(ctx, "alice", a.Run.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("test adapter claimed durability")
	}
}

func TestConcurrentAcceptAndCancel(t *testing.T) {
	m := New(nil)
	req := request(t)
	var wg sync.WaitGroup
	ids := make(chan string, 64)
	for range 64 {
		wg.Go(func() {
			a, err := m.Accept(ctx, "alice", key, req)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- a.Run.ID
		})
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate run")
		}
		id = got
	}
	for range 64 {
		wg.Go(func() {
			r, err := m.Cancel(ctx, "alice", id)
			if err != nil || r.State != "CANCELLING" || r.Revision != 2 {
				t.Errorf("non-idempotent cancel: %+v %v", r, err)
			}
		})
	}
	wg.Wait()
	r, err := m.Get(ctx, "alice", id)
	if err != nil {
		t.Fatal(err)
	}
	r = advance(t, m, r, "ABORTED")
	again, err := m.Cancel(ctx, "alice", id)
	if err != nil || again.Revision != r.Revision || again.State != "ABORTED" {
		t.Fatal("ABORTED cancel changed state")
	}
	*again.FinishedAt = time.Time{}
	stored, err := m.Get(ctx, "alice", id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FinishedAt.IsZero() {
		t.Fatal("returned pointer mutated stored run")
	}
}

func TestCancellationCompletionRace(t *testing.T) {
	for range 100 {
		m := New(nil)
		r := create(t, m).Run
		for _, state := range []string{"VALIDATING", "PROVISIONING", "RUNNING", "COLLECTING", "ANALYZING", "REPORTING"} {
			r = advance(t, m, r, state)
		}
		var cancelErr, completeErr error
		var wg sync.WaitGroup
		wg.Go(func() { _, cancelErr = m.Cancel(ctx, "alice", r.ID) })
		wg.Go(func() { _, completeErr = m.Advance(ctx, "alice", r.ID, r.Revision, run.Change{State: "COMPLETED"}) })
		wg.Wait()
		final, err := m.Get(ctx, "alice", r.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch final.State {
		case "COMPLETED":
			if completeErr != nil || !errors.Is(cancelErr, run.ErrTerminal) {
				t.Fatal("completion winner inconsistent")
			}
		case "CANCELLING":
			if cancelErr != nil || !errors.Is(completeErr, run.ErrRevision) {
				t.Fatal("cancel overwritten")
			}
		default:
			t.Fatal("invalid winner")
		}
		if final.Revision != r.Revision+1 {
			t.Fatal("both racers committed")
		}
	}
}

func TestRejectedAcceptDoesNotReserveKey(t *testing.T) {
	m := New(nil)
	bad := request(t)
	bad.Profile = ""
	if _, err := m.Accept(ctx, "alice", key, bad); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Accept(cancelled, "alice", key, request(t)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	create(t, m)
	if len(m.runs) != 1 || len(m.bindings) != 1 {
		t.Fatal("rejection reserved state")
	}
}
