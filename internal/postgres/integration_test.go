package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var testContext = context.Background()

// Create only a new random database on loopback. Never migrate, truncate or
// drop the database supplied by the user. Cleanup touches only our new database.
func testDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PERFENG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PERFENG_TEST_DATABASE_URL to a loopback PostgreSQL admin database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration (redacted)")
	}
	local := func(host string) bool { return host == "127.0.0.1" || host == "::1" || host == "localhost" }
	if !local(config.Host) {
		t.Fatal("integration tests require a loopback database host")
	}
	for _, fallback := range config.Fallbacks {
		if !local(fallback.Host) {
			t.Fatal("non-loopback fallback rejected")
		}
	}
	admin, err := Open(testContext, dsn)
	if err != nil {
		t.Fatal("cannot connect to test database; configuration redacted")
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	name := "perfeng_test_" + hex.EncodeToString(suffix[:])
	if !regexp.MustCompile(`^perfeng_test_[a-f0-9]{16}$`).MatchString(name) {
		t.Fatal("unsafe test database name")
	}
	ctx, cancel := context.WithTimeout(testContext, 15*time.Second)
	defer cancel()
	if _, err = admin.db.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		closeTest(t, admin)
		t.Fatal("test role needs CREATEDB permission")
	}
	// New connections use the same local endpoint and credentials, with no
	// inherited search_path or session options. Cleartext is test-loopback only.
	connection := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password),
		Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}
	t.Cleanup(func() {
		defer closeTest(t, admin)
		ctx, cancel := context.WithTimeout(testContext, 15*time.Second)
		defer cancel()
		if _, err := admin.db.ExecContext(ctx, "DROP DATABASE "+name); err != nil {
			t.Errorf("could not remove disposable database %s", name)
		}
	})
	return connection.String()
}

func openTest(t *testing.T, dsn string) *Repository {
	t.Helper()
	r, err := Open(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, r) })
	return r
}

func closeTest(t *testing.T, r *Repository) {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Error("could not close test database connection pool")
	}
}

func testRequest(t *testing.T) run.Request {
	t.Helper()
	b, err := contract.Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}
	var request run.Request
	if err = json.Unmarshal(b, &request); err != nil {
		t.Fatal(err)
	}
	return request
}
func accepted(t *testing.T, r *Repository, principal, key string) run.Accepted {
	t.Helper()
	a, err := r.Accept(testContext, principal, key, testRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func artifactFor(id string) run.Artifact {
	return run.Artifact{ID: "1cfa0000-0000-4000-8000-000000000001", RunID: id, Kind: "raw",
		URI: "s3://perfeng-artifacts/runs/" + id + "/summary.json", SHA256: strings.Repeat("a", 64),
		SizeBytes: 42, MediaType: "application/json", Format: "k6-summary/v1"}
}

func TestPostgresIntegration(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	other := openTest(t, dsn)
	if err := other.Migrate(testContext); err != nil {
		t.Fatal("migration not idempotent:", err)
	}
	request := testRequest(t)
	a := accepted(t, r, "alice", "request-key-00000001")
	if !contract.ValidID(a.Run.ID) || a.ExpiresAt.Sub(a.Run.CreatedAt) != 24*time.Hour {
		t.Fatal("invalid persisted acceptance")
	}
	current, err := r.Advance(testContext, "alice", a.Run.ID, 1, run.Change{State: "VALIDATING"})
	if err != nil {
		t.Fatal(err)
	}
	replay := accepted(t, other, "alice", "request-key-00000001")
	if replay != a {
		t.Fatal("original acceptance replay changed")
	}
	got, err := other.Get(testContext, "alice", a.Run.ID)
	if err != nil || got != current {
		t.Fatal("separate connection did not see committed state")
	}
	changed := request
	changed.Profile = "soak"
	if _, err := other.Accept(testContext, "alice", "request-key-00000001", changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal("missing payload conflict", err)
	}
	b := accepted(t, other, "bob", "request-key-00000001")
	if b.Run.ID == a.Run.ID {
		t.Fatal("principal key scope collision")
	}
	if _, err := r.Get(testContext, "bob", a.Run.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal read", err)
	}
	if _, err := r.Cancel(testContext, "bob", a.Run.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal cancellation", err)
	}
	art := artifactFor(a.Run.ID)
	if err := r.RegisterArtifact(testContext, "alice", art); err != nil {
		t.Fatal(err)
	}
	if err := other.RegisterArtifact(testContext, "alice", art); err != nil {
		t.Fatal("artifact retry", err)
	}
	gotArtifact, err := other.GetArtifact(testContext, "alice", a.Run.ID, art.ID)
	if err != nil || gotArtifact != art {
		t.Fatal("artifact not durable", err)
	}
	altered := art
	altered.SHA256 = strings.Repeat("b", 64)
	if err := r.RegisterArtifact(testContext, "alice", altered); !errors.Is(err, run.ErrArtifactConflict) {
		t.Fatal("artifact overwritten", err)
	}
	if _, err := r.GetArtifact(testContext, "bob", a.Run.ID, art.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("artifact visibility", err)
	}
	if err := r.RegisterArtifact(testContext, "bob", art); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("artifact ownership", err)
	}
	// An independent OS process cannot use the parent's pool or cached snapshot.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPostgresRestartChild$", "-test.v")
	command.Env = append(os.Environ(), "PERFENG_CHILD_DATABASE_URL="+dsn, "PERFENG_CHILD_RUN_ID="+a.Run.ID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restart child failed: %v\n%s", err, output)
	}
	r = openTest(t, dsn)
	other = openTest(t, dsn)
	t.Run("concurrent-create", func(t *testing.T) {
		var wg sync.WaitGroup
		ids := make(chan string, 32)
		for i := range 32 {
			repository := r
			if i%2 == 1 {
				repository = other
			}
			wg.Go(func() {
				a, err := repository.Accept(testContext, "concurrent", "request-key-00000002", request)
				if err != nil {
					t.Error(err)
					return
				}
				ids <- a.Run.ID
			})
		}
		wg.Wait()
		close(ids)
		first := ""
		for id := range ids {
			if first != "" && first != id {
				t.Fatal("duplicate acceptance")
			}
			first = id
		}
		var count int
		if err := r.db.QueryRow("SELECT count(*) FROM perfeng_control.runs WHERE principal='concurrent'").Scan(&count); err != nil || count != 1 {
			t.Fatal("duplicate row", err, count)
		}
	})
	t.Run("cancel-and-complete", func(t *testing.T) {
		for i := range 20 {
			a := accepted(t, r, "racer", fmt.Sprintf("request-key-race-%04d", i))
			current := a.Run
			for _, state := range []run.State{
				run.StateValidating,
				run.StateProvisioning,
				run.StateRunning,
				run.StateCollecting,
				run.StateAnalyzing,
				run.StateReporting,
			} {
				var err error
				current, err = r.Advance(testContext, "racer", current.ID, current.Revision, run.Change{State: state})
				if err != nil {
					t.Fatal(err)
				}
			}
			var cancelErr, finishErr error
			var wg sync.WaitGroup
			wg.Go(func() { _, cancelErr = r.Cancel(testContext, "racer", current.ID) })
			wg.Go(func() {
				_, finishErr = other.Advance(testContext, "racer", current.ID, current.Revision, run.Change{State: "COMPLETED"})
			})
			wg.Wait()
			final, err := r.Get(testContext, "racer", current.ID)
			if err != nil || final.Revision != current.Revision+1 {
				t.Fatal("race committed twice", err)
			}
			if final.State == "COMPLETED" {
				if finishErr != nil || !errors.Is(cancelErr, run.ErrTerminal) {
					t.Fatal("invalid completion winner")
				}
			} else if final.State == "CANCELLING" {
				if cancelErr != nil || !errors.Is(finishErr, run.ErrRevision) {
					t.Fatal("cancellation overwritten")
				}
				repeat, err := other.Cancel(testContext, "racer", current.ID)
				if err != nil || repeat != final {
					t.Fatal("cancel replay mutated")
				}
			} else {
				t.Fatal("unexpected terminal race state")
			}
		}
	})
	t.Run("expiry-retains-old-run", func(t *testing.T) {
		old := accepted(t, r, "expiry", "request-key-expiry")
		_, err := r.db.Exec("UPDATE perfeng_control.create_bindings SET expires_at=clock_timestamp()-interval '1 second' WHERE principal='expiry'")
		if err != nil {
			t.Fatal(err)
		}
		fresh := accepted(t, other, "expiry", "request-key-expiry")
		if old.Run.ID == fresh.Run.ID {
			t.Fatal("expired key replayed")
		}
		if _, err := r.Get(testContext, "expiry", old.Run.ID); err != nil {
			t.Fatal("old evidence lost", err)
		}
	})
	t.Run("rollback-no-orphan", func(t *testing.T) {
		_, err := r.db.Exec(`
			CREATE FUNCTION perfeng_control.reject_binding() RETURNS trigger LANGUAGE plpgsql AS
			$$ BEGIN RAISE EXCEPTION 'injected test failure' USING ERRCODE='23514'; END; $$;
			CREATE TRIGGER reject_binding BEFORE INSERT ON perfeng_control.create_bindings
			FOR EACH ROW EXECUTE FUNCTION perfeng_control.reject_binding()`)
		if err != nil {
			t.Fatal(err)
		}
		_, acceptErr := r.Accept(testContext, "rollback", "request-key-rollback", request)
		if _, err := r.db.Exec("DROP TRIGGER reject_binding ON perfeng_control.create_bindings; DROP FUNCTION perfeng_control.reject_binding()"); err != nil {
			t.Fatal(err)
		}
		if acceptErr == nil {
			t.Fatal("injected failure accepted")
		}
		var count int
		if err := r.db.QueryRow("SELECT count(*) FROM perfeng_control.runs WHERE principal='rollback'").Scan(&count); err != nil || count != 0 {
			t.Fatal("orphan run after rollback", err)
		}
		accepted(t, r, "rollback", "request-key-rollback")
	})
	t.Run("deadline-releases-key", func(t *testing.T) {
		tx, err := r.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec("SELECT pg_advisory_xact_lock($1)", scopeLock("deadline", "request-key-deadline")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(testContext, 100*time.Millisecond)
		defer cancel()
		if _, err := other.Accept(ctx, "deadline", "request-key-deadline", request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("lock wait did not preserve the caller deadline", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		accepted(t, other, "deadline", "request-key-deadline")
	})
	t.Run("checksum-drift", func(t *testing.T) {
		if _, err := r.db.Exec("UPDATE perfeng_control.schema_migrations SET sha256='altered'"); err != nil {
			t.Fatal(err)
		}
		if err := r.Migrate(testContext); err == nil {
			t.Fatal("modified migration accepted")
		}
	})
}

func TestStoragePreservesContextErrors(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)

	cancelled, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := Open(cancelled, dsn); !errors.Is(err, context.Canceled) {
		t.Fatal("connection did not preserve cancellation", err)
	}
	if _, err := r.Get(cancelled, "alice", "perf-20260903-120000-12345678"); !errors.Is(err, context.Canceled) {
		t.Fatal("storage did not preserve cancellation", err)
	}

	expired, cancel := context.WithDeadline(testContext, time.Now().Add(-time.Second))
	defer cancel()
	if _, err := r.Get(expired, "alice", "perf-20260903-120000-12345678"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("storage did not preserve deadline", err)
	}
}

func TestConcurrentMigrationAndTerminalSnapshots(t *testing.T) {
	dsn := testDatabase(t)
	first, second := openTest(t, dsn), openTest(t, dsn)
	var wg sync.WaitGroup
	for _, repository := range []*Repository{first, second} {
		wg.Go(func() {
			if err := repository.Migrate(testContext); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	var count int
	if err := first.db.QueryRow("SELECT count(*) FROM perfeng_control.schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatal("concurrent migration duplicated ledger", err)
	}
	a := accepted(t, first, "terminal", "request-key-terminal")
	current, err := first.Advance(testContext, "terminal", a.Run.ID, 1, run.Change{State: "VALIDATING"})
	if err != nil {
		t.Fatal(err)
	}
	failure := &run.Failure{Code: "VALIDATION_FAILED", Message: "Approved resource is no longer available"}
	invalid, err := first.Advance(testContext, "terminal", current.ID, current.Revision, run.Change{State: "INVALID", Failure: failure})
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.Get(testContext, "terminal", current.ID)
	if err != nil || !reflect.DeepEqual(got, invalid) || got.FinishedAt == nil {
		t.Fatal("terminal snapshot changed in storage", err)
	}
	failure.Message = "mutated caller value"
	if _, err := second.Cancel(testContext, "terminal", current.ID); !errors.Is(err, run.ErrTerminal) {
		t.Fatal("invalid run cancelled", err)
	}
	if _, err := second.Advance(testContext, "terminal", current.ID, invalid.Revision, run.Change{State: "RUNNING"}); !errors.Is(err, run.ErrTransition) {
		t.Fatal("terminal resumed", err)
	}
	b := accepted(t, first, "abort", "request-key-aborted")
	cancelling, err := first.Cancel(testContext, "abort", b.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := 99
	aborted, err := second.Advance(testContext, "abort", b.Run.ID, cancelling.Revision, run.Change{State: "ABORTED", ToolExitCode: &code})
	if err != nil {
		t.Fatal(err)
	}
	again, err := first.Cancel(testContext, "abort", b.Run.ID)
	if err != nil || !reflect.DeepEqual(again, aborted) || *again.ToolExitCode != 99 {
		t.Fatal("aborted replay changed", err)
	}
	// Unknown versions, including an empty hash, must reject an older binary.
	if _, err := first.db.Exec("INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES ('9999_unknown.sql','')"); err != nil {
		t.Fatal(err)
	}
	if err := second.Migrate(testContext); err == nil {
		t.Fatal("unknown migration accepted")
	}
}

func TestPostgresRestartChild(t *testing.T) {
	dsn := os.Getenv("PERFENG_CHILD_DATABASE_URL")
	if dsn == "" {
		t.Skip("launched only by parent restart test")
	}
	r := openTest(t, dsn)
	id := os.Getenv("PERFENG_CHILD_RUN_ID")
	current, err := r.Get(testContext, "alice", id)
	if err != nil || current.State != "VALIDATING" || current.Revision != 2 {
		t.Fatal("current snapshot lost across process restart")
	}
	replay := accepted(t, r, "alice", "request-key-00000001")
	if replay.Run.ID != id || replay.Run.State != "CREATED" || replay.Run.Revision != 1 ||
		replay.ExpiresAt.Sub(replay.Run.CreatedAt) != 24*time.Hour {
		t.Fatal("acceptance binding lost across restart")
	}
	artifact := artifactFor(id)
	got, err := r.GetArtifact(testContext, "alice", id, artifact.ID)
	if err != nil || got != artifact {
		t.Fatal("artifact reference lost across restart")
	}
}

func TestMigrationRollback(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if _, err := r.db.Exec("CREATE SCHEMA perfeng_control; CREATE TABLE perfeng_control.artifacts(probe integer)"); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err == nil {
		t.Fatal("conflicting schema accepted")
	}
	var table sql.NullString
	if err := r.db.QueryRow("SELECT to_regclass('perfeng_control.runs')::text").Scan(&table); err != nil || table.Valid {
		t.Fatal("partial migration committed", err)
	}
	if err := r.db.QueryRow("SELECT to_regclass('perfeng_control.schema_migrations')::text").Scan(&table); err != nil || table.Valid {
		t.Fatal("partial ledger committed", err)
	}
}
