package authentication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const credentialPath = "credentials.json"

func TestStaticAuthenticatesExactTokenAndPermissions(t *testing.T) {
	source := credentialFixture(t, []credentialConfig{{
		TokenSHA256: tokenDigest("alice-token"),
		Principal:   "local-operator",
		Permissions: []string{
			permissionCreateRun,
			permissionReadRun,
			permissionCancelRun,
			permissionCreateBaseline,
			permissionReadBaseline,
			permissionTransitionBaseline,
		},
	}})
	authenticator, err := LoadStatic(source, credentialPath)
	if err != nil {
		t.Fatal(err)
	}

	identity, err := authenticator.Authenticate(context.Background(), "alice-token")
	if err != nil {
		t.Fatal(err)
	}
	want := httpapi.Identity{
		Principal:          "local-operator",
		CreateRun:          true,
		ReadRun:            true,
		CancelRun:          true,
		CreateBaseline:     true,
		ReadBaseline:       true,
		TransitionBaseline: true,
	}
	if identity != want {
		t.Fatalf("Authenticate() = %#v, want %#v", identity, want)
	}
	if _, err := authenticator.Authenticate(context.Background(), "Alice-token"); !errors.Is(
		err, ErrUnauthenticated,
	) {
		t.Fatalf("case-changed token error = %v", err)
	}
}

func TestStaticSupportsIdentityPreservingTokenRotation(t *testing.T) {
	permissions := []string{permissionReadRun, permissionCreateRun}
	source := credentialFixture(t, []credentialConfig{
		{TokenSHA256: tokenDigest("old-token"), Principal: "alice", Permissions: permissions},
		{
			TokenSHA256: tokenDigest("new-token"), Principal: "alice",
			Permissions: []string{permissionCreateRun, permissionReadRun},
		},
	})
	authenticator, err := LoadStatic(source, credentialPath)
	if err != nil {
		t.Fatal(err)
	}

	oldIdentity, oldErr := authenticator.Authenticate(context.Background(), "old-token")
	newIdentity, newErr := authenticator.Authenticate(context.Background(), "new-token")
	if oldErr != nil || newErr != nil || oldIdentity != newIdentity {
		t.Fatalf("rotated identities = %#v/%v and %#v/%v", oldIdentity, oldErr, newIdentity, newErr)
	}
}

func TestStaticAuthenticatesConcurrently(t *testing.T) {
	authenticator, err := LoadStatic(credentialFixture(t, []credentialConfig{{
		TokenSHA256: tokenDigest("token"), Principal: "alice", Permissions: []string{permissionReadRun},
	}}), credentialPath)
	if err != nil {
		t.Fatal(err)
	}

	errorsFound := make(chan error, 32)
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for range 100 {
				identity, err := authenticator.Authenticate(context.Background(), "token")
				if err != nil || identity.Principal != "alice" || !identity.ReadRun {
					errorsFound <- errors.New("concurrent authentication returned an unexpected result")
					return
				}
			}
		}()
	}
	callers.Wait()
	close(errorsFound)
	if err := <-errorsFound; err != nil {
		t.Fatal(err)
	}
}

func TestLoadStaticRejectsMalformedConfiguration(t *testing.T) {
	valid := credentialConfig{
		TokenSHA256: tokenDigest("token"), Principal: "alice",
		Permissions: []string{permissionReadRun},
	}
	tooMany := make([]credentialConfig, maximumCredentials+1)
	for index := range tooMany {
		suffix := strconv.Itoa(index)
		tooMany[index] = credentialConfig{
			TokenSHA256: tokenDigest("token-" + suffix),
			Principal:   "principal-" + suffix,
			Permissions: []string{permissionReadRun},
		}
	}
	for _, test := range []struct {
		name   string
		source fstest.MapFS
		path   string
	}{
		{name: "nil filesystem", path: credentialPath},
		{name: "invalid path", source: fstest.MapFS{}, path: `..\\credentials.json`},
		{name: "missing file", source: fstest.MapFS{}, path: credentialPath},
		{name: "empty credentials", source: documentFS(t, credentialDocument{SchemaVersion: 1})},
		{name: "wrong version", source: documentFS(t, credentialDocument{Credentials: []credentialConfig{valid}})},
		{name: "too many credentials", source: documentFS(t, credentialDocument{
			SchemaVersion: 1, Credentials: tooMany,
		})},
		{name: "invalid digest", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: "not-a-digest", Principal: "alice", Permissions: []string{permissionReadRun},
		}})},
		{name: "uppercase digest", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: strings.ToUpper(valid.TokenSHA256), Principal: "alice",
			Permissions: []string{permissionReadRun},
		}})},
		{name: "blank principal", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: valid.TokenSHA256, Principal: " ", Permissions: []string{permissionReadRun},
		}})},
		{name: "control in principal", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: valid.TokenSHA256, Principal: "alice\nadmin", Permissions: []string{permissionReadRun},
		}})},
		{name: "missing permission", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: valid.TokenSHA256, Principal: "alice",
		}})},
		{name: "unknown permission", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: valid.TokenSHA256, Principal: "alice", Permissions: []string{"runs:delete"},
		}})},
		{name: "duplicate permission", source: credentialFixture(t, []credentialConfig{{
			TokenSHA256: valid.TokenSHA256, Principal: "alice",
			Permissions: []string{permissionReadRun, permissionReadRun},
		}})},
		{name: "duplicate digest", source: credentialFixture(t, []credentialConfig{valid, valid})},
		{name: "conflicting rotation", source: credentialFixture(t, []credentialConfig{
			valid,
			{
				TokenSHA256: tokenDigest("replacement"), Principal: "alice",
				Permissions: []string{permissionCreateRun},
			},
		})},
		{name: "duplicate JSON field", source: fstest.MapFS{credentialPath: &fstest.MapFile{
			Data: []byte(`{"schemaVersion":1,"schemaVersion":1,"credentials":[]}`),
		}}},
		{name: "unknown JSON field", source: fstest.MapFS{credentialPath: &fstest.MapFile{
			Data: []byte(`{"schemaVersion":1,"credentials":[],"extra":true}`),
		}}},
		{name: "oversized file", source: fstest.MapFS{credentialPath: &fstest.MapFile{
			Data: []byte(strings.Repeat(" ", maximumCredentialFileBytes+1)),
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadStatic(test.source, test.path); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("LoadStatic() error = %v", err)
			}
		})
	}
}

func TestStaticRejectsUnsafeTokensAndPreservesCancellation(t *testing.T) {
	authenticator, err := LoadStatic(credentialFixture(t, []credentialConfig{{
		TokenSHA256: tokenDigest("token"), Principal: "alice", Permissions: []string{permissionReadRun},
	}}), credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "unknown", "has space", strings.Repeat("x", maximumTokenBytes+1)} {
		if _, err := authenticator.Authenticate(context.Background(), token); !errors.Is(
			err, ErrUnauthenticated,
		) {
			t.Fatalf("Authenticate(%q) error = %v", token, err)
		}
	}
	if _, err := (*Static)(nil).Authenticate(context.Background(), "token"); !errors.Is(
		err, ErrUnauthenticated,
	) {
		t.Fatalf("nil Authenticate() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authenticator.Authenticate(ctx, "token"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Authenticate() error = %v", err)
	}
}

func TestStaticDoesNotRetainOrDiscloseRawTokens(t *testing.T) {
	const token = "sensitive-raw-token"
	authenticator, err := LoadStatic(credentialFixture(t, []credentialConfig{{
		TokenSHA256: tokenDigest(token), Principal: "alice", Permissions: []string{permissionReadRun},
	}}), credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", authenticator), token) {
		t.Fatal("authenticator retained the raw token")
	}
	_, err = authenticator.Authenticate(context.Background(), token+"-invalid")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("authentication error disclosed token bytes")
	}
}

func credentialFixture(t *testing.T, credentials []credentialConfig) fstest.MapFS {
	t.Helper()

	return documentFS(t, credentialDocument{SchemaVersion: 1, Credentials: credentials})
}

func documentFS(t *testing.T, document credentialDocument) fstest.MapFS {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return fstest.MapFS{credentialPath: &fstest.MapFile{Data: data, Mode: 0o400}}
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))

	return hex.EncodeToString(digest[:])
}
