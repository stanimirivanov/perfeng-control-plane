package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestStorageErrorClassification(t *testing.T) {
	if storageError(nil) != nil {
		t.Fatal("nil error changed")
	}
	if !errors.Is(storageError(sql.ErrNoRows), run.ErrNotFound) {
		t.Fatal("missing row classification")
	}
	for _, code := range []string{"08006", "53300", "57P01", "55P03", "40001", "40P01", "23505"} {
		err := storageError(&pgconn.PgError{Code: code, Message: "secret", Detail: "password=secret"})
		if !errors.Is(err, run.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or wrong SQLSTATE %s", code)
		}
	}
	err := storageError(&pgconn.PgError{Code: "23514", Message: "secret", Detail: "row contains secret"})
	if errors.Is(err, run.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if !errors.Is(storageError(context.DeadlineExceeded), run.ErrUnavailable) {
		t.Fatal("deadline not retryable")
	}
}
func TestScopeEncoding(t *testing.T) {
	if scopeLock("a", "bc") == scopeLock("ab", "c") {
		t.Fatal("ambiguous scope encoding")
	}
	if scopeLock("alice", "key") != scopeLock("alice", "key") {
		t.Fatal("unstable lock")
	}
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("empty DSN accepted")
	}
}
