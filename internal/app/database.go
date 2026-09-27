package app

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Database is the durable persistence boundary shared by the HTTP API and
// reconciliation worker. The owner must close it during process shutdown.
type Database interface {
	httpapi.Repository
	httpapi.NativeWorkerPresenceStore
	nativeworker.LeaseStore
	nativeworker.RenewalStore
	nativeworker.ReleaseStore
	nativeworker.UploadGrantStore
	nativeworker.ArtifactRegistrationStore
	nativeworker.TerminalStore
	run.ArtifactRepository
	run.ReconciliationStore
	kubernetes.ExecutionStore
	Close() error
}

var _ Database = (*postgres.Repository)(nil)

type openDatabase func(context.Context, string) (Database, error)

// NewDatabase opens the configured PostgreSQL repository without running migrations.
func NewDatabase(ctx context.Context, config DatabaseConfig) (Database, error) {
	return newDatabase(ctx, config, openPostgres)
}

func newDatabase(
	ctx context.Context,
	config DatabaseConfig,
	open openDatabase,
) (Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Validate() != nil || open == nil {
		return nil, ErrInvalidConfiguration
	}

	database, err := open(ctx, config.URL)
	if err != nil {
		return nil, classifyDependencyInitialization(ctx, err)
	}
	if database == nil {
		return nil, ErrDependencyInitialization
	}

	return database, nil
}

func openPostgres(ctx context.Context, databaseURL string) (Database, error) {
	return postgres.Open(ctx, databaseURL)
}
