package postgres_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// This external-package test exercises app composition without a postgres -> app
// production dependency. Keep its database random, disposable and loopback-only.
func identityCompositionDatabase(t *testing.T) (*postgres.Repository, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("PERFENG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PERFENG_TEST_DATABASE_URL to a loopback PostgreSQL admin database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid disposable database configuration")
	}
	local := func(host string) bool { return host == "127.0.0.1" || host == "::1" || host == "localhost" }
	if !local(config.Host) {
		t.Fatal("database host must be loopback")
	}
	for _, f := range config.Fallbacks {
		if !local(f.Host) {
			t.Fatal("database fallback must be loopback")
		}
	}
	ctx := context.Background()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to disposable PostgreSQL")
	}
	t.Cleanup(func() {
		if err := admin.Close(ctx); err != nil {
			t.Error("admin cleanup failed")
		}
	})
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "perfeng_identity_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal("cannot create isolated database")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+name); err != nil {
			t.Error("isolated database cleanup failed")
		}
	})
	config = config.Copy()
	config.Database = name
	writer, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot connect to isolated database")
	}
	t.Cleanup(func() {
		if err := writer.Close(ctx); err != nil {
			t.Error("writer cleanup failed")
		}
	})
	repository, err := postgres.Open(ctx, (&url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password), Host: net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port))), Path: "/" + name, RawQuery: "sslmode=disable"}).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return repository, writer
}

type identityCompositionStore struct {
	repository       *postgres.Repository
	block            atomic.Bool
	entered, release chan struct{}
}

func (s *identityCompositionStore) IdentityAdmitted(ctx context.Context, a, c serviceaccess.Identity, k string) (bool, error) {
	allowed, err := s.repository.IdentityAdmitted(ctx, a, c, k)
	if s.block.Load() {
		close(s.entered)
		<-s.release
	}
	return allowed, err
}

func TestNamedIdentityCompositionAgainstTLSAndPostgres(t *testing.T) {
	repository, writer := identityCompositionDatabase(t)
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nextKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var seconds atomic.Int64
	seconds.Store(time.Now().Unix())
	now := func() time.Time { return time.Unix(seconds.Load(), 0) }
	var rotation, outage atomic.Bool
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/realm/keys" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected key request")
			w.WriteHeader(400)
			return
		}
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		kid := "initial"
		publicKey := &key.PublicKey
		if rotation.Load() {
			kid = "rotated"
			publicKey = &nextKey.PublicKey
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid, "n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()), "e": "AQAB"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	issuer := server.URL + "/realm"
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	store := &identityCompositionStore{repository: repository, entered: make(chan struct{}), release: make(chan struct{})}
	clients := map[string]string{"delegate": "HUMAN", "ci": "CI"}
	runtime, err := app.NewNamedIdentityAuthentication(app.NamedIdentityConfig{Profile: authentication.IdentityProfile{Issuer: issuer, Audience: "api", Clients: clients}, JWKSEndpoint: issuer + "/keys", Roots: roots}, store, now)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	if requests.Load() != 0 {
		t.Fatal("construction fetched keys")
	}
	clients["delegate"] = "CI"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := writer.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, binding := range []struct{ subject, kind, client string }{{"maya", "HUMAN", "delegate"}, {"pipeline", "CI", "ci"}} {
		exec(`INSERT INTO perfeng_control.identity_enrollments VALUES ($1,$2,$3,true,'review/356')`, issuer, binding.subject, binding.kind)
		exec(`INSERT INTO perfeng_control.identity_delegations VALUES ($1,$2,$3,true,'review/356')`, issuer, binding.subject, "client:"+binding.client)
	}
	token := func(subject, client, kid, iss string) string {
		t.Helper()
		signed := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": iss, "sub": subject, "aud": []string{"api"}, "azp": client, "typ": "Bearer", "iat": now().Unix(), "exp": now().Add(time.Minute).Unix()})
		signed.Header["kid"] = kid
		signingKey := key
		if kid == "rotated" {
			signingKey = nextKey
		}
		value, err := signed.SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	check := func(credential string, want error) authentication.VerifiedIdentity {
		t.Helper()
		got, err := runtime.AuthenticateIdentity(ctx, credential)
		if !errors.Is(err, want) {
			t.Fatalf("authentication = %v, want %v", err, want)
		}
		if err != nil && got != (authentication.VerifiedIdentity{}) {
			t.Fatal("partial identity on failure")
		}
		return got
	}
	for _, binding := range []struct{ subject, kind, client string }{{"maya", "HUMAN", "delegate"}, {"pipeline", "CI", "ci"}} {
		got := check(token(binding.subject, binding.client, "initial", issuer), nil)
		if got.Actor.ID != binding.subject || got.Actor.Issuer != issuer || got.ActorKind != binding.kind || got.Caller != (serviceaccess.Identity{Issuer: issuer, ID: "client:" + binding.client}) {
			t.Fatal("actor/caller substitution", got)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("key snapshot was not shared")
	}
	if allowed, err := repository.HasServicePermission(ctx, serviceaccess.Identity{Issuer: "urn:service", ID: "search"}, serviceaccess.Identity{Issuer: issuer, ID: "maya"}, serviceaccess.Rerun); err != nil || allowed {
		t.Fatal("identity granted service authority", err)
	}
	for _, table := range []string{"identity_enrollments", "identity_delegations"} {
		credential := token("maya", "delegate", "initial", issuer)
		exec("UPDATE perfeng_control." + table + " SET enabled=false WHERE actor_id='maya'")
		check(credential, authentication.ErrUnauthenticated)
		exec("UPDATE perfeng_control." + table + " SET enabled=true WHERE actor_id='maya'")
		check(credential, nil)
	}
	check(token("maya", "ci", "initial", issuer), authentication.ErrUnauthenticated)
	check(token("maya", "delegate", "initial", "https://other.example"), authentication.ErrUnauthenticated)
	check("not.a.token", authentication.ErrUnauthenticated)
	check(token("maya", "delegate", "unknown", issuer), authentication.ErrUnauthenticated)
	rotation.Store(true)
	seconds.Add(11)
	check(token("maya", "delegate", "rotated", issuer), nil)
	check(token("maya", "delegate", "initial", issuer), authentication.ErrUnauthenticated)
	outage.Store(true)
	seconds.Add(301)
	check(token("maya", "delegate", "rotated", issuer), run.ErrUnavailable)
	outage.Store(false)
	seconds.Add(11)
	credential := token("maya", "delegate", "rotated", issuer)
	check(credential, nil)
	store.block.Store(true)
	finished := make(chan error, 1)
	go func() {
		got, err := runtime.AuthenticateIdentity(ctx, credential)
		if got != (authentication.VerifiedIdentity{}) {
			t.Error("shutdown returned identity")
		}
		finished <- err
	}()
	<-store.entered
	runtime.Stop()
	runtime.Stop()
	close(store.release)
	if err := <-finished; !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("in-flight shutdown", err)
	}
	count := requests.Load()
	check(credential, run.ErrUnavailable)
	if requests.Load() != count {
		t.Fatal("stopped runtime fetched keys")
	}
}
