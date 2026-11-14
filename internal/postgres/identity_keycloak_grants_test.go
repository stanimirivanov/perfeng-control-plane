package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// Qualify current authority with real credentials, not token roles or a cached
// admission result. These private probes are not public endpoints or mutations.
func qualifyKeycloakServiceGrants(t *testing.T, ctx context.Context, runtime *app.NamedIdentityAuthentication, repository *postgres.Repository, writer *pgx.Conn, issuer, humanID, ciID, humanToken, ciToken string) {
	t.Helper()
	service := serviceaccess.Identity{Issuer: "urn:fixture", ID: "service"}
	human := serviceaccess.Identity{Issuer: issuer, ID: humanID}
	ci := serviceaccess.Identity{Issuer: issuer, ID: ciID}
	caller := serviceaccess.Identity{Issuer: issuer, ID: "client:perfeng-console-delegate"}
	write := func(query string, args ...any) {
		t.Helper()
		if _, err := writer.Exec(ctx, query, args...); err != nil {
			t.Fatal("service qualification setup failed")
		}
	}
	// Only the disposable schema-owner session provisions reviewed assignments.
	for _, s := range []serviceaccess.Identity{service, {Issuer: "urn:foreign", ID: service.ID}} {
		write(`INSERT INTO perfeng_control.service_administrators VALUES ($1,$2,session_user)`, s.Issuer, s.ID)
	}
	grant := func(s, actor serviceaccess.Identity, permission serviceaccess.Permission, enabled bool) {
		t.Helper()
		write(`SELECT perfeng_control.set_service_grant($1,$2,$3,$4,$5,$6,'qualification/356/grants')`, s.Issuer, s.ID, actor.Issuer, actor.ID, string(permission), enabled)
	}
	content, err := contract.Files.ReadFile("snapshot/examples/create.json")
	var request run.Request
	if err != nil || json.Unmarshal(content, &request) != nil {
		t.Fatal("cannot read pinned Run admission fixture")
	}
	const owner = "qualification-ci-owner"
	accepted, err := repository.Accept(ctx, owner, "qualification-356-grants", request, run.ExecutionTargetKubernetes)
	if err != nil {
		t.Fatal("cannot admit isolated qualification Run")
	}
	// This is a reviewed test binding, not proof of public CI creation/execution.
	write(`INSERT INTO perfeng_control.run_services
		(run_id,owner_principal,service_issuer,service_id,creator_issuer,creator_id,creator_kind,review_ref)
		VALUES ($1,$2,$3,$4,$5,$6,'CI','qualification/356/grants')`, accepted.Run.ID, owner, service.Issuer, service.ID, ci.Issuer, ci.ID)

	handler, err := app.NewNamedIdentityBoundary(true, runtime, func(w http.ResponseWriter, r *http.Request, identity httpapi.NamedIdentity) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("credential reached service probe")
		}
		if r.URL.Path == "/read" {
			binding, err := repository.GetServiceRunBinding(r.Context(), identity.Actor, accepted.Run.ID)
			if errors.Is(err, run.ErrNotFound) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if err != nil {
				t.Fatal("current Run binding read failed")
			}
			if binding.Service != service || binding.Creator != ci || binding.CreatorKind != "CI" || binding.OwnerPrincipal != owner {
				t.Fatal("service, original CI creator or owner changed with reader")
			}
		} else {
			permission := serviceaccess.Rerun
			if r.URL.Path == "/approve" {
				permission = serviceaccess.ApproveBaseline
			}
			allowed, err := repository.HasServicePermission(r.Context(), service, identity.Actor, permission)
			if err != nil {
				t.Fatal("current permission read failed")
			}
			if !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err != nil {
		t.Fatal("cannot bind service qualification callback")
	}
	probe := func(label, path, token string, status int) {
		t.Helper()
		r := httptest.NewRequest("POST", "https://fixture.invalid"+path, strings.NewReader(`{"actor":"qualification-ci-owner","service":"foreign"}`)).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Perfeng-Actor", owner)
		r.Header.Set("X-Perfeng-Service", "foreign")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != status || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: service probe status/evidence differs: got %d, want %d", label, response.Code, status)
		}
	}
	probe("human has no initial read", "/read", humanToken, 404)
	probe("CI creator has no implicit service read", "/read", ciToken, 404)
	probe("human has no initial rerun", "/rerun", humanToken, 403)
	grant(serviceaccess.Identity{Issuer: "urn:foreign", ID: service.ID}, human, serviceaccess.ReadRun, true)
	grant(service, caller, serviceaccess.ReadRun, true)
	grant(service, ci, serviceaccess.ReadRun, true)
	probe("foreign service, caller and creator rights do not transfer", "/read", humanToken, 404)
	probe("CI explicit service read", "/read", ciToken, 204)
	grant(service, human, serviceaccess.Rerun, true)
	probe("explicit rerun probe", "/rerun", humanToken, 204)
	probe("rerun does not imply read", "/read", humanToken, 404)
	probe("rerun does not imply baseline approval", "/approve", humanToken, 403)
	grant(service, human, serviceaccess.ReadRun, true)
	probe("human current read with original CI binding", "/read", humanToken, 204)
	grant(service, human, serviceaccess.ReadRun, false)
	probe("committed read revocation next request", "/read", humanToken, 404)
	probe("independent rerun survives read revocation", "/rerun", humanToken, 204)
	if _, err := repository.Get(ctx, owner, accepted.Run.ID); err != nil {
		t.Fatal("legacy owner fixture unexpectedly disappeared")
	}
	grant(service, human, serviceaccess.ReadRun, true)
	probe("explicit read restoration", "/read", humanToken, 204)
	grant(service, human, serviceaccess.Rerun, false)
	probe("committed rerun revocation next request", "/rerun", humanToken, 403)
	probe("independent read survives rerun revocation", "/read", humanToken, 204)
	probe("baseline approval remains independent", "/approve", humanToken, 403)
	grant(service, human, serviceaccess.ApproveBaseline, true)
	probe("explicit baseline approval probe", "/approve", humanToken, 204)
	probe("baseline approval does not restore rerun", "/rerun", humanToken, 403)
	grant(service, human, serviceaccess.ApproveBaseline, false)
	probe("committed baseline approval revocation", "/approve", humanToken, 403)
	// Assert the exact accountable ordered mutation trail, not merely a row count.
	rows, err := writer.Query(ctx, `SELECT operation,permission,administrator::text = session_user::text,review_ref
		FROM perfeng_control.service_access_audit WHERE service_issuer=$1 AND service_id=$2 AND actor_issuer=$3 AND actor_id=$4 AND permission IS NOT NULL ORDER BY sequence`, service.Issuer, service.ID, human.Issuer, human.ID)
	if err != nil {
		t.Fatal("cannot read qualification grant audit")
	}
	defer rows.Close()
	for _, expected := range []struct{ operation, permission string }{{"GRANT", "run:rerun"}, {"GRANT", "run:read"}, {"REVOKE", "run:read"}, {"GRANT", "run:read"}, {"REVOKE", "run:rerun"}, {"GRANT", "baseline:approve"}, {"REVOKE", "baseline:approve"}} {
		var operation, permission, review string
		var accountable bool
		if !rows.Next() || rows.Scan(&operation, &permission, &accountable, &review) != nil || operation != expected.operation || permission != expected.permission || !accountable || review != "qualification/356/grants" {
			t.Fatal("service grant audit differs from declared sequence")
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected service grant audit")
	}
}
