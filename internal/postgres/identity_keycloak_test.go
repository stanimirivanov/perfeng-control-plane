package postgres_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestKeycloakIssuerBoundary(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:12345/realms/perfeng-fixture", "https://foreign.example/realms/perfeng-fixture", "https://127.0.0.1:12345/other", "https://user@127.0.0.1:12345/realms/perfeng-fixture", "https://127.0.0.1:12345/realms/perfeng-fixture?query"} {
		if validKeycloakFixtureIssuer(value) {
			t.Fatal("foreign issuer accepted")
		}
	}
	if !validKeycloakFixtureIssuer("https://127.0.0.1:12345/realms/perfeng-fixture") {
		t.Fatal("fixture issuer refused")
	}
}

//go:embed testdata/keycloak-fixture.json
var keycloakFixturePin []byte

//go:embed testdata/keycloak_bridge.py
var keycloakFixtureBridge []byte

func validKeycloakFixtureIssuer(value string) bool {
	u, err := url.Parse(value)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/realms/perfeng-fixture" && !strings.ContainsAny(value, "?#")
}

// Materialize only the reviewed immutable blobs, never the sibling working tree.
func stageKeycloakFixture(t *testing.T, source, target string) {
	t.Helper()
	var pin struct {
		Commit string
		Files  map[string]string
	}
	if json.Unmarshal(keycloakFixturePin, &pin) != nil || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(pin.Commit) {
		t.Fatal("invalid fixture pin")
	}
	for path, digest := range pin.Files {
		if !filepath.IsLocal(path) {
			t.Fatal("unsafe fixture path")
		}
		content, err := exec.Command("git", "--no-pager", "-C", source, "show", pin.Commit+":"+path).Output()
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(content)) != digest {
			t.Fatal("pinned fixture input unavailable or mismatched")
		}
		destination := filepath.Join(target, path)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			t.Fatal("cannot stage fixture")
		}
		if err := os.WriteFile(destination, content, 0o600); err != nil {
			t.Fatal("cannot stage fixture")
		}
	}
	if err := os.WriteFile(filepath.Join(target, "bridge.py"), keycloakFixtureBridge, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A hard timeout must not strand an owned provider. The private observer records
// the random ownership label before create, including ambiguous create outcomes.
func cleanupKeycloakFixture(t *testing.T, stateFile string) {
	t.Helper()
	content, err := os.ReadFile(stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var state struct{ Nonce, Root string }
	if err != nil || json.Unmarshal(content, &state) != nil || !regexp.MustCompile(`^[a-f0-9]{16}$`).MatchString(state.Nonce) {
		t.Error("invalid fixture cleanup identity")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=perfeng.keycloak.fixture="+state.Nonce, "--filter", "name=^/perfeng-keycloak-"+state.Nonce+"$").Output()
	if err != nil {
		t.Error("fixture cleanup observation failed")
		return
	}
	for _, id := range strings.Fields(string(ids)) {
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
			t.Error("unsafe fixture cleanup ID")
			return
		}
		if exec.CommandContext(ctx, "docker", "rm", "--force", id).Run() != nil {
			t.Error("owned fixture cleanup failed")
		}
	}
	if !regexp.MustCompile(`^/dev/shm/perfeng-keycloak-[a-z0-9_]{8}$`).MatchString(state.Root) {
		t.Error("invalid private fixture directory")
		return
	}
	info, err := os.Lstat(state.Root)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Error("private fixture ownership changed")
		return
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		t.Error("foreign private fixture directory")
		return
	}
	if err := os.RemoveAll(state.Root); err != nil {
		t.Error("private fixture cleanup failed")
	}

}

func TestNamedIdentityAgainstRealKeycloak(t *testing.T) {
	if os.Getenv("PERFENG_TEST_KEYCLOAK") != "1" {
		t.Skip("explicit identity-qualification gate required")
	}
	source := os.Getenv("PERFENG_ENVIRONMENT_SOURCE")
	if source == "" || os.Getenv("PERFENG_TEST_DATABASE_URL") == "" {
		t.Fatal("pinned fixture source and disposable database required")
	}
	repository, writer := identityCompositionDatabase(t)
	directory := t.TempDir()
	stageKeycloakFixture(t, source, directory)
	stateFile := filepath.Join(directory, "ownership.json")
	defer cleanupKeycloakFixture(t, stateFile)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "uv", "run", "--locked", "--offline", "--project", directory, "python", filepath.Join(directory, "bridge.py"), stateFile)
	command.Dir = directory
	command.Stderr = io.Discard
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 20 * time.Second
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal("cannot open fixture pipe")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal("cannot open fixture pipe")
	}
	if err := command.Start(); err != nil {
		t.Fatal("cannot start pinned fixture")
	}
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			cancel()
			_ = command.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 96<<10)
	if !scanner.Scan() {
		t.Fatal("real fixture did not supply private token envelope")
	}
	var batch struct{ Issuer, RootPEM, CI, Initial, HumanID, Delegated string }
	if json.Unmarshal(scanner.Bytes(), &batch) != nil || !validKeycloakFixtureIssuer(batch.Issuer) {
		t.Fatal("invalid fixture envelope")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(batch.RootPEM)) {
		t.Fatal("invalid fixture CA")
	}
	clients := map[string]string{"perfeng-ci": "CI", "perfeng-console-delegate": "HUMAN"}
	config := app.NamedIdentityConfig{Profile: authentication.IdentityProfile{Issuer: batch.Issuer, Audience: "perfeng-control-plane", Clients: clients}, JWKSEndpoint: batch.Issuer + "/protocol/openid-connect/certs", Roots: roots}
	runtime, err := app.NewNamedIdentityAuthentication(config, repository, time.Now)
	if err != nil {
		t.Fatal("cannot compose real identity verifier")
	}
	defer runtime.Stop()
	human := "e3295d37-a188-4753-b89c-1ca188d85102"
	ci := "e3295d37-a188-4753-b89c-1ca188d85101"
	execPolicy := func(query string, args ...any) {
		t.Helper()
		if _, err := writer.Exec(ctx, query, args...); err != nil {
			t.Fatal("fixture policy write failed")
		}
	}
	for _, binding := range []struct{ id, kind, client string }{{ci, "CI", "perfeng-ci"}, {human, "HUMAN", "perfeng-console-delegate"}} {
		execPolicy(`INSERT INTO perfeng_control.identity_enrollments VALUES ($1,$2,$3,true,'qualification/356')`, batch.Issuer, binding.id, binding.kind)
		execPolicy(`INSERT INTO perfeng_control.identity_delegations VALUES ($1,$2,$3,true,'qualification/356')`, batch.Issuer, binding.id, "client:"+binding.client)
	}
	check := func(token string, want error) authentication.VerifiedIdentity {
		t.Helper()
		got, err := runtime.AuthenticateIdentity(ctx, token)
		if !errors.Is(err, want) {
			t.Fatal("real-token outcome differs from declared expectation")
		}
		if err != nil && got != (authentication.VerifiedIdentity{}) {
			t.Fatal("partial identity on refusal")
		}
		return got
	}
	for _, tc := range []struct{ token, id, kind, client string }{{batch.CI, ci, "CI", "perfeng-ci"}, {batch.Delegated, human, "HUMAN", "perfeng-console-delegate"}} {
		got := check(tc.token, nil)
		if got.Actor != (serviceaccess.Identity{Issuer: batch.Issuer, ID: tc.id}) || got.ActorKind != tc.kind || got.Caller != (serviceaccess.Identity{Issuer: batch.Issuer, ID: "client:" + tc.client}) {
			t.Fatal("real actor/caller/kind mismatch")
		}
	}
	check(batch.Initial, authentication.ErrUnauthenticated)
	check(batch.HumanID, authentication.ErrUnauthenticated)
	// Change an actual signature byte without rewriting identity expectations.
	parts := strings.Split(batch.Delegated, ".")
	if len(parts) != 3 || len(parts[2]) < 2 {
		t.Fatal("invalid actual JWT")
	}
	first := "A"
	if parts[2][:1] == first {
		first = "B"
	}
	parts[2] = first + parts[2][1:]
	check(strings.Join(parts, "."), authentication.ErrUnauthenticated)
	for _, table := range []string{"identity_enrollments", "identity_delegations"} {
		execPolicy("UPDATE perfeng_control."+table+" SET enabled=false WHERE actor_id=$1", human)
		check(batch.Delegated, authentication.ErrUnauthenticated)
		execPolicy("UPDATE perfeng_control."+table+" SET enabled=true WHERE actor_id=$1", human)
		check(batch.Delegated, nil)
	}
	runtime.Stop()
	if _, err := runtime.AuthenticateIdentity(ctx, batch.Delegated); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("stopped real verifier admitted identity")
	}
	wrong := config
	wrong.Profile.Clients = map[string]string{"perfeng-console-delegate": "CI"}
	wrongRuntime, err := app.NewNamedIdentityAuthentication(wrong, repository, time.Now)
	if err != nil {
		t.Fatal("cannot compose kind-mismatch test")
	}
	_, wrongErr := wrongRuntime.AuthenticateIdentity(ctx, batch.Delegated)
	wrongRuntime.Stop()
	if !errors.Is(wrongErr, authentication.ErrUnauthenticated) {
		t.Fatal("kind mismatch admitted")
	}
	allowed, err := repository.HasServicePermission(ctx, serviceaccess.Identity{Issuer: "urn:fixture", ID: "service"}, serviceaccess.Identity{Issuer: batch.Issuer, ID: human}, serviceaccess.ApproveBaseline)
	if err != nil || allowed {
		t.Fatal("identity inherited baseline authority")
	}
	if _, err := io.WriteString(stdin, "VERIFIED\n"); err != nil {
		t.Fatal("cannot acknowledge verifier result")
	}
	_ = stdin.Close()
	if !scanner.Scan() {
		t.Fatal("fixture did not complete cleanup and redacted receipt")
	}
	redacted := append([]byte(nil), scanner.Bytes()...)
	if bytes.Contains(redacted, []byte("eyJ")) || bytes.Contains(redacted, []byte("PRIVATE KEY")) {
		t.Fatal("credential in receipt")
	}
	var receipt struct {
		Admission                    string
		PassedChecks, RemainingGates []string
	}
	if json.Unmarshal(redacted, &receipt) != nil || receipt.Admission != "NOT_ADMITTED" || !slices.Contains(receipt.PassedChecks, "control-plane-real-token-conformance") || slices.Contains(receipt.RemainingGates, "control-plane-real-token-conformance") {
		t.Fatal("invalid qualification receipt")
	}
	if scanner.Scan() || scanner.Err() != nil {
		t.Fatal("unexpected fixture output")
	}
	err = command.Wait()
	waited = true
	if err != nil {
		t.Fatal("fixture failed or cleanup was incomplete")
	}
	t.Log(string(redacted))
}
