package postgres

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Record statement roles, not SQL arguments (which include lease capabilities).
type reconciliationTrace struct {
	mu         sync.Mutex
	statements []string
}

func (trace *reconciliationTrace) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	query := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	role := query

	switch {
	case strings.HasPrefix(query, "begin"):
		role = "begin"
	case strings.HasPrefix(query, "set local"):
		role = "configure"
	case strings.Contains(query, "for update of r skip locked"):
		role = "candidates"
	case query == "select clock_timestamp()":
		role = "clock"
	case strings.HasPrefix(query, "select expires_at"):
		role = "eligibility"
	case strings.HasPrefix(query, "insert into perfeng_control.reconciliation_leases"):
		role = "claim"
	case strings.Contains(query, "for update"):
		role = "run-lock"
	case strings.HasPrefix(query, "select worker_id"):
		role = "owner"
	case strings.HasPrefix(query, "update perfeng_control.reconciliation_leases set expires_at"):
		role = "lease-update"
	case strings.HasPrefix(query, "update perfeng_control.runs set snapshot"):
		role = "run-update"
	}

	trace.mu.Lock()
	trace.statements = append(trace.statements, role)
	trace.mu.Unlock()

	return ctx
}

func (*reconciliationTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (trace *reconciliationTrace) expect(t *testing.T, want ...string) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()

	if !reflect.DeepEqual(trace.statements, want) {
		t.Fatalf("statement order = %v, want %v", trace.statements, want)
	}
	trace.statements = nil
}

// Statement ordering is part of the concurrency contract, including a separate
// database-clock read for every candidate and no commits inside batch helpers.
func TestReconciliationStatementOrder(t *testing.T) {
	setup, dsn := claimsDB(t)
	accepted(t, setup, "alice", "request-key-order-a")
	accepted(t, setup, "bob", "request-key-order-b")

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	trace := &reconciliationTrace{}
	config.Tracer = trace
	r := &Repository{db: stdlib.OpenDB(*config)}
	t.Cleanup(func() { closeTest(t, r) })

	claims, err := r.ClaimRuns(testContext, "order-worker", 2, time.Minute)
	if err != nil || len(claims) != 2 {
		t.Fatal("expected two claims", err, len(claims))
	}
	trace.expect(t,
		"begin", "configure", "candidates",
		"clock", "eligibility", "claim",
		"clock", "eligibility", "claim",
		"commit",
	)

	empty, err := r.ClaimRuns(testContext, "other-worker", 2, time.Minute)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("expected a non-nil empty batch while leases are live", err)
	}
	trace.expect(t, "begin", "configure", "candidates", "commit")

	renewed, err := r.RenewClaim(testContext, claims[0].Lease, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	trace.expect(t, "begin", "configure", "run-lock", "owner", "clock", "lease-update", "commit")

	current, err := r.AdvanceClaim(testContext, renewed.Lease, renewed.Run.Revision, run.Change{State: "VALIDATING"})
	if err != nil {
		t.Fatal(err)
	}
	trace.expect(t, "begin", "configure", "run-lock", "owner", "clock", "run-update", "commit")

	cancelled, err := setup.Cancel(testContext, renewed.Lease.Principal, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdvanceClaim(testContext, renewed.Lease, cancelled.Revision, run.Change{State: "ABORTED"}); err != nil {
		t.Fatal(err)
	}
	trace.expect(t,
		"begin", "configure", "run-lock", "owner", "clock", "run-update", "lease-update", "commit",
	)

	if err := r.ReleaseClaim(testContext, claims[1].Lease, 0); err != nil {
		t.Fatal(err)
	}
	trace.expect(t, "begin", "configure", "run-lock", "owner", "clock", "lease-update", "commit")
}
