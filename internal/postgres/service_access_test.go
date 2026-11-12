package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// Each login is unique to a disposable database. No existing login is altered.
func serviceLogin(t *testing.T, owner *Repository, dsn, suffix string) *Repository {
	t.Helper()
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid disposable database address")
	}
	role := "svc_" + strings.TrimPrefix(address.Path, "/") + suffix
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err := owner.db.Exec("CREATE ROLE " + quoted + " LOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.db.Exec("DROP OWNED BY " + quoted); err != nil {
			t.Error(err)
		}
		if _, err := owner.db.Exec("DROP ROLE " + quoted); err != nil {
			t.Error(err)
		}
	})
	if _, err := owner.db.Exec("GRANT USAGE ON SCHEMA perfeng_control TO " + quoted); err != nil {
		t.Fatal(err)
	}
	address.User = url.User(role)

	return openTest(t, address.String())
}

func serviceGrant(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, service, actor, permission string, enabled bool) error {
	t.Helper()
	_, err := db.Exec(`SELECT perfeng_control.set_service_grant('urn:services',$1,'urn:people',$2,$3,$4,'review/330')`, service, actor, permission, enabled)
	return err
}

func bindReviewedRun(t *testing.T, r *Repository, runID string) {
	t.Helper()
	_, err := r.db.Exec(`INSERT INTO perfeng_control.run_services
        (run_id, owner_principal, service_issuer, service_id, creator_issuer, creator_id, creator_kind, review_ref)
        SELECT run_id, principal, 'urn:services', 'search', 'urn:ci', 'pipeline', 'CI', 'review/330'
        FROM perfeng_control.runs WHERE run_id=$1`, runID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceAccessUpgradeAndImmutableOwnership(t *testing.T) {
	r := openTest(t, testDatabase(t))
	migrateBeforeVerification(t, r, "0025_")
	original := accepted(t, r, "ci-owner", "service-bind-key01").Run
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.run_services`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("implicit backfill: %d, %v", count, err)
	}
	if _, err := r.Get(testContext, "maya", original.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("legacy visibility changed", err)
	}
	bindReviewedRun(t, r, original.ID)
	for _, statement := range []string{
		`UPDATE perfeng_control.run_services SET service_id='billing'`,
		`DELETE FROM perfeng_control.run_services`,
		`TRUNCATE perfeng_control.run_services`,
		`UPDATE perfeng_control.service_access_audit SET review_ref='forged'`,
		`DELETE FROM perfeng_control.service_access_audit`,
		`TRUNCATE perfeng_control.service_access_audit`,
	} {
		if _, err := r.db.Exec(statement); err == nil {
			t.Fatal("immutable evidence changed", statement)
		}
	}
	retained, err := r.Get(testContext, "ci-owner", original.ID)
	if err != nil || retained.ID != original.ID {
		t.Fatal("owner read changed", err)
	}
	if _, err := r.Get(testContext, "maya", original.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("binding activated new visibility", err)
	}
}

func TestServiceGrantsAreScopedAuditedAndRevocable(t *testing.T) {
	dsn := testDatabase(t)
	owner := openTest(t, dsn)
	if err := owner.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	admin := serviceLogin(t, owner, dsn, "admin")
	var administrator string
	if err := admin.db.QueryRow(`SELECT session_user`).Scan(&administrator); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.db.Exec(`INSERT INTO perfeng_control.service_administrators VALUES ('urn:services','search',$1)`, administrator); err != nil {
		t.Fatal(err)
	}
	function := `perfeng_control.set_service_grant(text,text,text,text,text,boolean,text)`
	if _, err := owner.db.Exec("GRANT EXECUTE ON FUNCTION " + function + " TO " + pgx.Identifier{administrator}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if err := serviceGrant(t, admin.db, "search", "maya", "run:read", true); err != nil {
		t.Fatal(err)
	}
	if err := serviceGrant(t, admin.db, "billing", "maya", "run:read", true); err == nil {
		t.Fatal("cross-service administrator accepted")
	}
	for _, statement := range []string{
		`INSERT INTO perfeng_control.service_administrators VALUES ('urn:services','billing',session_user)`,
		`UPDATE perfeng_control.service_grants SET enabled=false`,
		`DELETE FROM perfeng_control.service_access_audit`,
	} {
		if _, err := admin.db.Exec(statement); err == nil {
			t.Fatal("administrator bypassed protected path")
		}
	}
	runtime := serviceLogin(t, owner, dsn, "runtime")
	if err := serviceGrant(t, runtime.db, "search", "maya", "run:read", true); err == nil {
		t.Fatal("ordinary runtime login changed grant")
	}
	if err := serviceGrant(t, admin.db, "search", "maya", "baseline:write", true); err == nil {
		t.Fatal("unknown permission accepted")
	}
	tx, err := admin.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Error("transaction cleanup failed", err)
		}
	}()
	if _, err := tx.Exec("SET LOCAL synchronous_commit=off"); err != nil {
		t.Fatal(err)
	}
	if err := serviceGrant(t, tx, "search", "maya", "run:read", false); err != nil {
		t.Fatal(err)
	}
	// NOWAIT proves the assignment lock without sleeps or timing assertions.
	var locked int
	err = owner.db.QueryRow(`SELECT 1 FROM perfeng_control.service_administrators WHERE service_id='search' FOR UPDATE NOWAIT`).Scan(&locked)
	var lockError *pgconn.PgError
	if !errors.As(err, &lockError) || lockError.Code != "55P03" {
		t.Fatal("administrator assignment was not locked during grant change", err)
	}
	var synchronous string
	if err := tx.QueryRow("SHOW synchronous_commit").Scan(&synchronous); err != nil || synchronous != "on" {
		t.Fatal("grant change did not require WAL flush", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertGrant := func(enabled bool, auditCount int) {
		t.Helper()
		var actual bool
		var count int
		if err := owner.db.QueryRow(`SELECT enabled FROM perfeng_control.service_grants WHERE service_id='search' AND actor_id='maya' AND permission='run:read'`).Scan(&actual); err != nil || actual != enabled {
			t.Fatalf("grant state %t: %v", actual, err)
		}
		if err := owner.db.QueryRow(`SELECT count(*) FROM perfeng_control.service_access_audit WHERE administrator=$1`, administrator).Scan(&count); err != nil || count != auditCount {
			t.Fatalf("audit count %d, want %d: %v", count, auditCount, err)
		}
	}
	assertGrant(true, 1)
	if err := serviceGrant(t, admin.db, "search", "maya", "run:read", true); err != nil {
		t.Fatal(err)
	}
	assertGrant(true, 1)
	if err := serviceGrant(t, admin.db, "search", "maya", "run:read", false); err != nil {
		t.Fatal(err)
	}
	assertGrant(false, 2)
	var wait sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wait.Go(func() { failures <- serviceGrant(t, admin.db, "search", "maya", "run:read", true) })
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertGrant(true, 3)
}

func TestServiceAccessReadsUseCurrentActorAndRetainedBinding(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	second := openTest(t, dsn)
	runID := accepted(t, first, "ci-owner", "service-read-key01").Run.ID
	service := serviceaccess.Identity{Issuer: "urn:services", ID: "search"}
	maya := serviceaccess.Identity{Issuer: "urn:people", ID: "maya"}
	var owner string
	if err := first.db.QueryRow(`SELECT session_user`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.Exec(`INSERT INTO perfeng_control.service_administrators VALUES ('urn:services','search',$1)`, owner); err != nil {
		t.Fatal(err)
	}
	if err := serviceGrant(t, first.db, "search", "maya", "run:read", true); err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetServiceRunBinding(testContext, maya, runID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("unmapped historical Run visible", err)
	}
	bindReviewedRun(t, first, runID)
	binding, err := second.GetServiceRunBinding(testContext, maya, runID)
	if err != nil || binding.Service != service || binding.Creator.ID != "pipeline" || binding.OwnerPrincipal != "ci-owner" || binding.CreatorKind != "CI" {
		t.Fatal("creator/reader/service conflated", binding, err)
	}
	for _, permission := range []serviceaccess.Permission{serviceaccess.Rerun, serviceaccess.ApproveBaseline} {
		allowed, err := second.HasServicePermission(testContext, service, maya, permission)
		if err != nil || allowed {
			t.Fatal("read implicitly conferred another right", err)
		}
	}
	if allowed, err := second.HasServicePermission(testContext, serviceaccess.Identity{Issuer: "urn:other", ID: "search"}, maya, serviceaccess.ReadRun); err != nil || allowed {
		t.Fatal("service issuer omitted", err)
	}
	if _, err := second.GetServiceRunBinding(testContext, serviceaccess.Identity{Issuer: "urn:other", ID: "maya"}, runID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("actor issuer omitted", err)
	}
	if err := serviceGrant(t, first.db, "search", "maya", "run:read", false); err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetServiceRunBinding(testContext, maya, runID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("revocation cached or owner fallback used", err)
	}
	for _, permission := range []serviceaccess.Permission{serviceaccess.ReadRun, serviceaccess.CreateRun, serviceaccess.CancelRun, serviceaccess.Rerun, serviceaccess.CreateBaseline, serviceaccess.QualifyBaseline, serviceaccess.ApproveBaseline, serviceaccess.RetireBaseline} {
		if err := serviceGrant(t, first.db, "search", "robot", string(permission), true); err != nil {
			t.Fatal(err)
		}
		if allowed, err := second.HasServicePermission(testContext, service, serviceaccess.Identity{Issuer: "urn:people", ID: "robot"}, permission); err != nil || !allowed {
			t.Fatal("SQL right differs from contract vocabulary", permission, err)
		}
	}
	reopened := openTest(t, dsn)
	if allowed, err := reopened.HasServicePermission(testContext, service, maya, serviceaccess.ReadRun); err != nil || allowed {
		t.Fatal("revocation lost after reopen", err)
	}
}

func TestServiceAccessReadValidationPrecedesStorage(t *testing.T) {
	actor := serviceaccess.Identity{Issuer: "urn:people", ID: "maya"}
	service := serviceaccess.Identity{Issuer: "urn:services", ID: "search"}
	empty := &Repository{}
	if _, err := empty.GetServiceRunBinding(testContext, serviceaccess.Identity{}, "run"); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if allowed, err := empty.HasServicePermission(testContext, service, actor, "run:*"); allowed || !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := empty.GetServiceRunBinding(ctx, actor, "run"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if allowed, err := empty.HasServicePermission(ctx, service, actor, serviceaccess.ReadRun); allowed || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
