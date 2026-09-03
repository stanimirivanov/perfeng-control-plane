package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		pgErr := &pgconn.PgError{Code: code, Message: "secret", Detail: "password=secret"}
		err := storageError(fmt.Errorf("driver operation: %w", pgErr))
		if !errors.Is(err, run.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or wrong SQLSTATE %s", code)
		}
	}
	err := storageError(&pgconn.PgError{Code: "23514", Message: "secret", Detail: "row contains secret"})
	if errors.Is(err, run.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		classified := storageError(fmt.Errorf("driver operation: %w", cause))
		if !errors.Is(classified, cause) || errors.Is(classified, run.ErrUnavailable) {
			t.Fatalf("context error lost: %v", cause)
		}
	}

	var postgresFailure *postgresError
	if !errors.As(err, &postgresFailure) || postgresFailure.sqlState != "23514" {
		t.Fatal("unexpected SQLSTATE was not classified safely")
	}

	malformed := storageError(&pgconn.PgError{
		Code:    "secret\n",
		Message: "password=secret",
		Detail:  "row contains secret",
	})
	if malformed.Error() != "postgres operation failed" {
		t.Fatal("malformed SQLSTATE leaked driver data")
	}
}
func TestScopeEncoding(t *testing.T) {
	if scopeLock("a", "bc") == scopeLock("ab", "c") {
		t.Fatal("ambiguous scope encoding")
	}
	first := scopeLock("alice", "key")
	if repeated := scopeLock("alice", "key"); repeated != first {
		t.Fatal("unstable lock")
	}
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("empty DSN accepted")
	}
}
