package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate is an explicit administrative operation, never automatic in Open.
// The version ledger and each migration commit in the same transaction.
// Concurrent migrators serialize on a transaction-scoped advisory lock.
func (r *Repository) Migrate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(70657266656)"); err != nil {
		return storageError(err)
	}
	if _, err = tx.ExecContext(ctx, `
		CREATE SCHEMA IF NOT EXISTS perfeng_control;
		CREATE TABLE IF NOT EXISTS perfeng_control.schema_migrations (
			version text PRIMARY KEY,
			sha256 text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		return storageError(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	expected := make(map[string]string, len(entries))
	for _, entry := range entries {
		b, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		hash := sha256.Sum256(b)
		expected[entry.Name()] = hex.EncodeToString(hash[:])
	}
	rows, err := tx.QueryContext(ctx, "SELECT version,sha256 FROM perfeng_control.schema_migrations ORDER BY version")
	if err != nil {
		return storageError(err)
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var version, hash string
		if err = rows.Scan(&version, &hash); err != nil {
			break
		}
		expectedHash, known := expected[version]
		if !known || expectedHash != hash {
			err = errors.New("database migration version or checksum does not match this binary")
			break
		}
		applied[version] = true
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return storageError(rowErr)
	}
	// ReadDir is lexical order; reject a ledger with gaps instead of guessing.
	missing := false
	for _, entry := range entries {
		if applied[entry.Name()] {
			if missing {
				return errors.New("database migration ledger has a gap")
			}
			continue
		}
		missing = true
		b, _ := migrations.ReadFile("migrations/" + entry.Name())
		if _, err = tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), storageError(err))
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES ($1,$2)", entry.Name(), expected[entry.Name()]); err != nil {
			return storageError(err)
		}
	}
	return storageError(tx.Commit())
}
