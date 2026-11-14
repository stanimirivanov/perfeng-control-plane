package postgres_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestKeycloakRotationPhaseRefusesOutOfOrderOrMissingToken(t *testing.T) {
	for _, input := range []string{`{"phase":"retired","ci":"token"}`, `{"phase":"overlap"}`, `{"phase":"overlap","ci":3}`} {
		scanner := bufio.NewScanner(strings.NewReader(input))
		if _, err := readKeycloakPhase(scanner, "overlap"); err == nil {
			t.Fatal("invalid phase admitted")
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(`{"phase":"overlap","ci":"signed.token.fixture"}`))
	if phase, err := readKeycloakPhase(scanner, "overlap"); err != nil || phase.CI != "signed.token.fixture" {
		t.Fatal("valid phase refused")
	}
}

type keycloakPhase struct{ Phase, CI string }

func readKeycloakPhase(scanner *bufio.Scanner, expected string) (keycloakPhase, error) {
	var phase keycloakPhase
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &phase) != nil || phase.Phase != expected || (expected != "stopped" && (phase.CI == "" || len(phase.CI) > 16384)) {
		return keycloakPhase{}, errors.New("private fixture phase refused")
	}
	return phase, nil
}

func requireKeycloakOutcome(t *testing.T, ctx context.Context, runtime *app.NamedIdentityAuthentication, token string, want error) {
	t.Helper()
	got, err := runtime.AuthenticateIdentity(ctx, token)
	if !errors.Is(err, want) {
		t.Fatal("key lifecycle outcome differs from declared expectation")
	}
	if err != nil && got != (authentication.VerifiedIdentity{}) {
		t.Fatal("partial rotation identity")
	}
}

func keycloakUnknownKid(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("invalid actual JWT")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal("invalid actual JWT header")
	}
	var fields map[string]any
	if json.Unmarshal(header, &fields) != nil {
		t.Fatal("invalid actual JWT header")
	}
	fields["kid"] = "fixture-missing-key-probe"
	header, err = json.Marshal(fields)
	if err != nil {
		t.Fatal("cannot construct unknown-kid probe")
	}
	parts[0] = base64.RawURLEncoding.EncodeToString(header)
	return strings.Join(parts, ".")
}

// Token claims are already signature-verified. Assert only the precondition that
// a later refusal is not explained by expiry/age under the approved clock bounds.
func requireKeycloakTokenStillInWindow(t *testing.T, token string, now time.Time) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("invalid actual token")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("invalid token dates")
	}
	var dates struct {
		Issued  int64 `json:"iat"`
		Expires int64 `json:"exp"`
	}
	if json.Unmarshal(body, &dates) != nil || now.After(time.Unix(dates.Expires, 0).Add(30*time.Second)) || now.After(time.Unix(dates.Issued, 0).Add(330*time.Second)) {
		t.Fatal("token outside approved window; key-trust result would be ambiguous")
	}
}

func qualifyKeycloakRotation(t *testing.T, ctx context.Context, scanner *bufio.Scanner, stdin io.Writer, runtime *app.NamedIdentityAuthentication, clock *atomic.Int64, old string) {
	t.Helper()
	overlap, err := readKeycloakPhase(scanner, "overlap")
	if err != nil {
		t.Fatal("real overlap checkpoint missing")
	}
	// The frozen test clock has not crossed the original attempt cooldown.
	requireKeycloakOutcome(t, ctx, runtime, overlap.CI, authentication.ErrUnauthenticated)
	requireKeycloakOutcome(t, ctx, runtime, old, nil)
	clock.Add(int64(11 * time.Second))
	requireKeycloakOutcome(t, ctx, runtime, overlap.CI, nil)
	requireKeycloakOutcome(t, ctx, runtime, old, nil)
	if _, err := io.WriteString(stdin, "OVERLAP_VERIFIED\n"); err != nil {
		t.Fatal("cannot acknowledge overlap")
	}
	retired, err := readKeycloakPhase(scanner, "retired")
	if err != nil {
		t.Fatal("real retirement checkpoint missing")
	}
	requireKeycloakOutcome(t, ctx, runtime, old, nil)
	clock.Add(int64(11 * time.Second))
	requireKeycloakOutcome(t, ctx, runtime, keycloakUnknownKid(t, retired.CI), authentication.ErrUnauthenticated)
	requireKeycloakTokenStillInWindow(t, old, time.Unix(0, clock.Load()))
	requireKeycloakOutcome(t, ctx, runtime, old, authentication.ErrUnauthenticated)
	requireKeycloakOutcome(t, ctx, runtime, retired.CI, nil)
	if _, err := io.WriteString(stdin, "RETIREMENT_VERIFIED\n"); err != nil {
		t.Fatal("cannot acknowledge retirement")
	}
}

func qualifyKeycloakOutage(t *testing.T, ctx context.Context, scanner *bufio.Scanner, stdin io.Writer, config app.NamedIdentityConfig, store authentication.IdentityPolicyStore) {
	t.Helper()
	ready, err := readKeycloakPhase(scanner, "outage-ready")
	if err != nil {
		t.Fatal("outage checkpoint missing")
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	runtime, err := app.NewNamedIdentityAuthentication(config, store, now)
	if err != nil {
		t.Fatal("cannot compose outage verifier")
	}
	defer runtime.Stop()
	requireKeycloakOutcome(t, ctx, runtime, ready.CI, nil)
	if _, err := io.WriteString(stdin, "STOP_ISSUER\n"); err != nil {
		t.Fatal("cannot select owned outage")
	}
	if _, err := readKeycloakPhase(scanner, "stopped"); err != nil {
		t.Fatal("issuer stop was not confirmed")
	}
	requireKeycloakOutcome(t, ctx, runtime, ready.CI, nil)
	clock.Add(int64(11 * time.Second))
	requireKeycloakOutcome(t, ctx, runtime, keycloakUnknownKid(t, ready.CI), run.ErrUnavailable)
	requireKeycloakOutcome(t, ctx, runtime, ready.CI, nil)
	clock.Add(int64(291 * time.Second))
	requireKeycloakTokenStillInWindow(t, ready.CI, now())
	requireKeycloakOutcome(t, ctx, runtime, ready.CI, run.ErrUnavailable)
	requireKeycloakHTTP(t, ctx, runtime, ready.CI, 503, authentication.VerifiedIdentity{})
	if _, err := io.WriteString(stdin, "OUTAGE_VERIFIED\n"); err != nil {
		t.Fatal("cannot acknowledge stale trust refusal")
	}
}
