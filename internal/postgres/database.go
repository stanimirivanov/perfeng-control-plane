// Package postgres implements durable control-plane storage in PostgreSQL 17+.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const operationTimeout = 15 * time.Second

type Repository struct{ db *sql.DB }

var _ run.Repository = (*Repository)(nil)

type postgresError struct {
	sqlState string
}

func (err *postgresError) Error() string {
	return fmt.Sprintf("postgres operation failed (SQLSTATE %s)", err.sqlState)
}

func validSQLState(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, character := range code {
		if (character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

// Open establishes a bounded connection pool without running migrations.
// DSNs are secrets: callers must not log them or driver connection errors.
// The owner must Close the repository when its service shuts down.
func Open(ctx context.Context, dsn string) (*Repository, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("database connection configuration is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, storageError(err)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, storageError(err)
	}
	// Acceptance must wait for local WAL flush, even when the connection
	// inherited an asynchronous-commit setting. Server fsync is a deployment duty.
	if _, err = tx.ExecContext(ctx, "SET LOCAL synchronous_commit = on; SET LOCAL lock_timeout = '5s'"); err != nil {
		_ = tx.Rollback()
		return nil, storageError(err)
	}
	return tx, nil
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return run.ErrNotFound
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if !validSQLState(pg.Code) {
			return errors.New("postgres operation failed")
		}
		if strings.HasPrefix(pg.Code, "08") || strings.HasPrefix(pg.Code, "53") ||
			strings.HasPrefix(pg.Code, "57") || pg.Code == "55P03" ||
			pg.Code == "40001" || pg.Code == "40P01" || pg.Code == "23505" {
			return run.ErrUnavailable
		}
		return &postgresError{sqlState: pg.Code}
	}
	// Unclassified connection and ambiguous commit failures are retryable only with
	// the same idempotency key. No automatic replay of a possibly committed write.
	return run.ErrUnavailable
}
