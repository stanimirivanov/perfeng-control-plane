package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestIdentityPolicyUpgradeRevocationAndIsolation(t *testing.T) {
	r := openTest(t, testDatabase(t))
	migrateBeforeVerification(t, r, "0029_")
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	policy, err := authentication.NewStoredIdentityPolicy(r)
	if err != nil {
		t.Fatal(err)
	}
	identity := authentication.VerifiedIdentity{
		Actor:  serviceaccess.Identity{Issuer: "https://identity.example/realm", ID: "maya"},
		Caller: serviceaccess.Identity{Issuer: "https://identity.example/realm", ID: "client:delegate"}, ActorKind: "HUMAN",
	}
	check := func(want error) {
		t.Helper()
		if err := policy.AuthorizeIdentity(testContext, identity); !errors.Is(err, want) {
			t.Fatalf("authorization = %v, want %v", err, want)
		}
	}
	exec := func(query string) {
		t.Helper()
		if _, err := r.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	check(authentication.ErrUnauthenticated)
	exec(`INSERT INTO perfeng_control.identity_enrollments VALUES ('https://identity.example/realm','maya','HUMAN',true,'review/356')`)
	check(authentication.ErrUnauthenticated)
	exec(`INSERT INTO perfeng_control.identity_delegations VALUES ('https://identity.example/realm','maya','client:delegate',true,'review/356')`)
	check(nil)
	identity.ActorKind = "CI"
	check(authentication.ErrUnauthenticated)
	identity.ActorKind = "HUMAN"
	identity.Caller.Issuer = "https://other.example/realm"
	check(authentication.ErrUnauthenticated)
	identity.Caller.Issuer = identity.Actor.Issuer
	identity.Caller.ID = "client:other"
	check(authentication.ErrUnauthenticated)
	identity.Caller.ID = "client:delegate"
	for _, table := range []string{"identity_enrollments", "identity_delegations"} {
		exec("UPDATE perfeng_control." + table + " SET enabled=false, review_ref='review/revoke'")
		check(authentication.ErrUnauthenticated)
		exec("UPDATE perfeng_control." + table + " SET enabled=true, review_ref='review/restore'")
		check(nil)
	}
	identity.Actor.ID = "other"
	check(authentication.ErrUnauthenticated)
	identity.Actor.ID = "maya"
	identity.Actor.Issuer = "https://other.example/realm"
	identity.Caller.Issuer = identity.Actor.Issuer
	check(authentication.ErrUnauthenticated)
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.identity_policy_audit WHERE administrator=session_user AND review_ref IN ('review/356','review/revoke','review/restore')`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("audit count = %d: %v", count, err)
	}
}

func TestIdentityPolicyConstraintsAndRuntimeAuthority(t *testing.T) {
	dsn := testDatabase(t)
	owner := openTest(t, dsn)
	if err := owner.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.db.Exec(`INSERT INTO perfeng_control.identity_enrollments VALUES ('https://identity.example','maya','HUMAN',true,'review/356');
        INSERT INTO perfeng_control.identity_delegations VALUES ('https://identity.example','maya','client:delegate',true,'review/356')`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE perfeng_control.identity_enrollments SET actor_kind='CI'`,
		`UPDATE perfeng_control.identity_enrollments SET actor_id='other'`,
		`UPDATE perfeng_control.identity_delegations SET caller_id='client:other'`,
		`UPDATE perfeng_control.identity_enrollments SET review_ref=''`,
		`DELETE FROM perfeng_control.identity_enrollments`,
		`DELETE FROM perfeng_control.identity_delegations`,
		`TRUNCATE perfeng_control.identity_enrollments CASCADE`,
		`TRUNCATE perfeng_control.identity_delegations`,
		`UPDATE perfeng_control.identity_policy_audit SET review_ref='forged'`,
		`DELETE FROM perfeng_control.identity_policy_audit`,
		`TRUNCATE perfeng_control.identity_policy_audit`,
		`INSERT INTO perfeng_control.identity_delegations VALUES ('https://identity.example','absent','client:delegate',true,'review/356')`,
	} {
		if _, err := owner.db.Exec(query); err == nil {
			t.Fatalf("constraint accepted %s", query)
		}
	}
	runtime := serviceLogin(t, owner, dsn, "identity")
	var login string
	if err := runtime.db.QueryRow(`SELECT session_user`).Scan(&login); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.db.Exec(`GRANT SELECT ON perfeng_control.identity_enrollments, perfeng_control.identity_delegations TO ` + pgx.Identifier{login}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	actor := serviceaccess.Identity{Issuer: "https://identity.example", ID: "maya"}
	caller := serviceaccess.Identity{Issuer: actor.Issuer, ID: "client:delegate"}
	if allowed, err := runtime.IdentityAdmitted(testContext, actor, caller, "HUMAN"); err != nil || !allowed {
		t.Fatalf("read-only runtime = %v, %v", allowed, err)
	}
	for _, query := range []string{
		`UPDATE perfeng_control.identity_enrollments SET enabled=false`,
		`UPDATE perfeng_control.identity_delegations SET enabled=false`,
		`INSERT INTO perfeng_control.identity_policy_audit (issuer,actor_id,actor_kind,enabled,review_ref) VALUES ('https://identity.example','forged','HUMAN',true,'forged')`,
	} {
		if _, err := runtime.db.Exec(query); err == nil {
			t.Fatal("runtime wrote policy", query)
		}
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := runtime.IdentityAdmitted(ctx, actor, caller, "HUMAN"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.IdentityAdmitted(testContext, actor, caller, "HUMAN"); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("outage = %v", err)
	}
}

func TestIdentityPolicyCommittedRevocationAcrossConcurrentReaders(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO perfeng_control.identity_enrollments VALUES ('https://identity.example','pipeline','CI',true,'review/356');
 INSERT INTO perfeng_control.identity_delegations VALUES ('https://identity.example','pipeline','client:ci',true,'review/356')`); err != nil {
		t.Fatal(err)
	}
	actor := serviceaccess.Identity{Issuer: "https://identity.example", ID: "pipeline"}
	caller := serviceaccess.Identity{Issuer: actor.Issuer, ID: "client:ci"}
	tx, err := r.db.BeginTx(testContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE perfeng_control.identity_delegations SET enabled=false,review_ref='review/revoke'`); err != nil {
		t.Fatal(err)
	}
	if allowed, err := r.IdentityAdmitted(testContext, actor, caller, "CI"); err != nil || !allowed {
		t.Fatalf("uncommitted revocation = %v, %v", allowed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 12 {
		readers.Go(func() {
			if allowed, err := r.IdentityAdmitted(testContext, actor, caller, "CI"); err != nil || allowed {
				t.Errorf("committed revocation = %v, %v", allowed, err)
			}
		})
	}
	readers.Wait()
}
