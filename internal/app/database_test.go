package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type databaseStub struct {
	Database
	closeErr error
}

func (database *databaseStub) Close() error {
	return database.closeErr
}

func TestNewDatabaseOpensConfiguredRepository(t *testing.T) {
	config := DatabaseConfig{URL: "postgres://service:secret@postgres/perfeng"}
	want := &databaseStub{}
	calls := 0
	open := openDatabase(func(ctx context.Context, databaseURL string) (Database, error) {
		calls++
		if ctx.Err() != nil || databaseURL != config.URL {
			t.Fatal("database opener received changed configuration")
		}

		return want, nil
	})

	database, err := newDatabase(context.Background(), config, open)
	if err != nil {
		t.Fatal(err)
	}
	if database != want || calls != 1 {
		t.Fatalf("database = %v, calls = %d", database, calls)
	}
}

func TestNewDatabaseRejectsConfigurationAndDependencies(t *testing.T) {
	valid := DatabaseConfig{URL: "postgres://postgres/perfeng"}
	tests := map[string]struct {
		config DatabaseConfig
		open   openDatabase
		want   error
	}{
		"configuration": {open: acceptingDatabaseOpener, want: ErrInvalidConfiguration},
		"opener":        {config: valid, want: ErrInvalidConfiguration},
		"nil database": {
			config: valid,
			open: func(context.Context, string) (Database, error) {
				return nil, nil
			},
			want: ErrDependencyInitialization,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newDatabase(context.Background(), test.config, test.open)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewDatabasePreservesCancellationAndRedactsErrors(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	openCalled := false
	open := openDatabase(func(context.Context, string) (Database, error) {
		openCalled = true

		return &databaseStub{}, nil
	})
	if _, err := newDatabase(
		canceled,
		DatabaseConfig{URL: "postgres://postgres/perfeng"},
		open,
	); !errors.Is(err, context.Canceled) || openCalled {
		t.Fatalf("error = %v, opener called = %t", err, openCalled)
	}

	secret := "postgres://service:secret@postgres/perfeng"
	open = func(context.Context, string) (Database, error) {
		return nil, errors.New("dial failed for " + secret)
	}
	_, err := newDatabase(context.Background(), DatabaseConfig{URL: secret}, open)
	if !errors.Is(err, ErrDependencyInitialization) || strings.Contains(err.Error(), secret) ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v", err)
	}
}

func acceptingDatabaseOpener(context.Context, string) (Database, error) {
	return &databaseStub{}, nil
}
