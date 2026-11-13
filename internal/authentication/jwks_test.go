package authentication

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func jwksFixture(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "current", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	if err != nil {
		t.Fatal(err)
	}
	return key, string(data)
}
func jwksTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestJWKSFetchTrustedTLSFeedsRealSignatureVerification(t *testing.T) {
	key, document := jwksFixture(t)
	_, claims, now := identityFixture()
	server := jwksTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/keys" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected authority or request")
		}
		w.Header().Set("Content-Type", "application/jwk-set+json")
		if _, err := io.WriteString(w, document); err != nil {
			t.Error(err)
		}
	}))
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	fetcher, err := NewIdentityJWKSFetcher(server.URL+"/realms/perfeng", server.URL+"/keys", roots, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fetcher.FetchIdentityKeys(context.Background())
	if err != nil || !snapshot.FetchedAt.Equal(now) || snapshot.Keys["current"].N.Cmp(key.N) != 0 {
		t.Fatal("trusted snapshot refused", err)
	}
	claims.Issuer = server.URL + "/realms/perfeng"
	verifier, err := NewJWTIdentityVerifier(claims.Issuer, claims.Audience[0], identityKeysFunc(func(context.Context, string) (IdentitySigningKey, error) {
		return IdentitySigningKey{PublicKey: snapshot.Keys["current"], FetchedAt: snapshot.FetchedAt}, nil
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token := signIdentityJWT(t, key, `{"alg":"RS256","typ":"JWT","kid":"current"}`, jwtPayload(t, claims))
	if _, err = verifier.VerifyIdentityCredential(context.Background(), token); err != nil {
		t.Fatal("fetched key failed signature verification", err)
	}
	snapshot.Keys["current"].N.SetInt64(1)
	again, err := fetcher.FetchIdentityKeys(context.Background())
	if err != nil || again.Keys["current"].N.Cmp(key.N) != 0 {
		t.Fatal("snapshots share mutable keys", err)
	}
}

func TestJWKSFetchRefusesUntrustedTLSRedirectsAndUnsafeResponses(t *testing.T) {
	_, document := jwksFixture(t)
	_, _, now := identityFixture()
	var destinationHits atomic.Int32
	destination := jwksTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
	for _, mode := range []string{"ca", "redirect", "status", "media", "encoding", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			server := jwksTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "redirect":
					http.Redirect(w, r, destination.URL, http.StatusFound)
					return
				case "status":
					w.WriteHeader(500)
					return
				case "media":
					w.Header().Set("Content-Type", "text/plain")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "oversized":
					if _, err := io.WriteString(w, strings.Repeat("x", 65537)); err != nil {
						return
					}
					return
				}
				if _, err := io.WriteString(w, document); err != nil {
					t.Error(err)
				}
			}))
			roots := x509.NewCertPool()
			if mode != "ca" {
				roots.AddCert(server.Certificate())
			}
			fetcher, err := NewIdentityJWKSFetcher(server.URL, server.URL+"/keys", roots, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			if value, err := fetcher.FetchIdentityKeys(context.Background()); err != run.ErrUnavailable || len(value.Keys) != 0 {
				t.Fatal("unsafe response trusted or details leaked", err)
			}
		})
	}
	if destinationHits.Load() != 0 {
		t.Fatal("redirect destination contacted")
	}
}

func TestJWKSFetchCancellationAndConfiguration(t *testing.T) {
	_, _, now := identityFixture()
	started := make(chan struct{})
	server := jwksTLSServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, endpoint := range []string{"http://example.test/keys", "https://other.test/keys", server.URL + "/keys?", server.URL + "/keys#"} {
		if _, err := NewIdentityJWKSFetcher(server.URL, endpoint, roots, func() time.Time { return now }); !errors.Is(err, run.ErrValidation) {
			t.Fatal("unsafe endpoint admitted", err)
		}
	}
	fetcher, err := NewIdentityJWKSFetcher(server.URL, server.URL+"/keys", roots, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := fetcher.FetchIdentityKeys(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled fetch did not stop")
	}
}

func TestJWKSDecoderRejectsAmbiguousPrivateOrMalformedKeys(t *testing.T) {
	_, document := jwksFixture(t)
	for _, mode := range []string{"valid", "mixed", "duplicate-kid", "duplicate-field", "private", "weak", "exponent", "key-ops", "empty", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			var root map[string][]map[string]any
			if err := json.Unmarshal([]byte(document), &root); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "mixed":
				root["keys"] = append(root["keys"], map[string]any{"kty": "EC", "kid": "other", "alg": "ES256", "use": "sig"})
			case "duplicate-kid":
				root["keys"] = append(root["keys"], root["keys"][0])
			case "private":
				root["keys"][0]["d"] = "secret"
			case "weak":
				root["keys"][0]["n"] = "Aw"
			case "exponent":
				root["keys"][0]["e"] = "AAEAAQ"
			case "key-ops":
				root["keys"][0]["key_ops"] = []string{"sign"}
			case "empty":
				root["keys"] = nil
			}
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "duplicate-field" {
				data = []byte(strings.Replace(string(data), `"kid":`, `"kid":"duplicate","kid":`, 1))
			}
			if mode == "oversized" {
				data = []byte(strings.Repeat("x", 65537))
			}
			keys, err := decodeIdentityJWKS(data)
			if mode == "valid" || mode == "mixed" {
				if err != nil || len(keys) != 1 {
					t.Fatal("supported key refused", err)
				}
			} else if err != run.ErrUnavailable || len(keys) != 0 {
				t.Fatal("malformed trust document accepted", err)
			}
		})
	}
}

func TestJWKSFetchDoesNotRenewTrustAgeAfterLongAttempt(t *testing.T) {
	_, document := jwksFixture(t)
	_, _, now := identityFixture()
	calls := 0
	server := jwksTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, document); err != nil {
			t.Error(err)
		}
	}))
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	fetcher, err := NewIdentityJWKSFetcher(server.URL, server.URL+"/keys", roots, func() time.Time {
		calls++
		if calls == 1 {
			return now
		}
		return now.Add(6 * time.Minute)
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := fetcher.FetchIdentityKeys(context.Background()); err != run.ErrUnavailable || len(value.Keys) != 0 {
		t.Fatal("long attempt manufactured fresh trust", err)
	}
}
