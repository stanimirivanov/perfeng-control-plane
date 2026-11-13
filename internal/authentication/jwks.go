package authentication

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumIdentityJWKSBytes = 64 << 10

// IdentityKeySnapshot is an independently owned complete key set. FetchedAt is
// the successful attempt's start time, conservatively bounding trust age; it
// never comes from a response header or cache-read time.
type IdentityKeySnapshot struct {
	Keys      map[string]*rsa.PublicKey
	FetchedAt time.Time
}

// IdentityJWKSFetcher fetches only a fixed trusted endpoint. It intentionally
// does not implement per-kid lookup: bounded refresh/cache policy must compose it.
type IdentityJWKSFetcher struct {
	endpoint string
	client   *http.Client
	now      func() time.Time
}

// NewIdentityJWKSFetcher requires explicit root trust and the issuer's exact
// HTTPS host/port. It clones root trust, disables proxies/redirects/compression,
// and bounds requests to three seconds, headers to 16 KiB and bodies to 64 KiB.
func NewIdentityJWKSFetcher(issuer, endpoint string, roots *x509.CertPool, now func() time.Time) (*IdentityJWKSFetcher, error) {
	if !validIdentityIssuer(issuer) || !validIdentityIssuer(endpoint) || roots == nil || now == nil {
		return nil, run.ErrValidation
	}
	origin, _ := url.Parse(issuer)
	target, _ := url.Parse(endpoint)
	if origin.Host != target.Host {
		return nil, run.ErrValidation
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots.Clone()}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16 << 10, DisableCompression: true, MaxConnsPerHost: 1, MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &IdentityJWKSFetcher{endpoint: endpoint, client: client, now: now}, nil
}

// CloseIdleConnections releases pooled idle connections without preventing new
// fetches. The owner should call it when retiring this long-lived fetcher.
func (fetcher *IdentityJWKSFetcher) CloseIdleConnections() { fetcher.client.CloseIdleConnections() }

// FetchIdentityKeys accepts no token/issuer/key-ID input and sends no credentials.
// Errors return no partial snapshot or backend/endpoint details. This is fetching,
// not discovery, caching, rotation admission or a production-enabled key source.
func (fetcher *IdentityJWKSFetcher) FetchIdentityKeys(ctx context.Context) (IdentityKeySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return IdentityKeySnapshot{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, fetcher.endpoint, nil)
	if err != nil {
		return IdentityKeySnapshot{}, run.ErrUnavailable
	}
	fetched := fetcher.now()
	response, err := fetcher.client.Do(request)
	if err != nil {
		return IdentityKeySnapshot{}, jwksFetchError(ctx)
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := response.Header.Get("Content-Encoding")
	if response.StatusCode != http.StatusOK || err != nil || (media != "application/json" && media != "application/jwk-set+json") || (encoding != "" && encoding != "identity") {
		return IdentityKeySnapshot{}, run.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumIdentityJWKSBytes+1))
	if err != nil {
		return IdentityKeySnapshot{}, jwksFetchError(ctx)
	}
	keys, err := decodeIdentityJWKS(data)
	if err != nil {
		return IdentityKeySnapshot{}, jwksFetchError(ctx)
	}
	if requestContext.Err() != nil {
		return IdentityKeySnapshot{}, jwksFetchError(ctx)
	}
	completed := fetcher.now()
	if fetched.IsZero() || fetched.After(completed) || completed.Sub(fetched) > 5*time.Minute {
		return IdentityKeySnapshot{}, run.ErrUnavailable
	}
	return IdentityKeySnapshot{Keys: keys, FetchedAt: fetched}, nil
}
func jwksFetchError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return run.ErrUnavailable
}

func decodeIdentityJWKS(data []byte) (map[string]*rsa.PublicKey, error) {
	if !jsondocument.Valid(data, maximumIdentityJWKSBytes) {
		return nil, run.ErrUnavailable
	}
	root, ok := jsondocument.ExactObject(data, "keys")
	if !ok {
		return nil, run.ErrUnavailable
	}
	var records []json.RawMessage
	if json.Unmarshal(root["keys"], &records) != nil || len(records) == 0 || len(records) > 32 {
		return nil, run.ErrUnavailable
	}
	keys := make(map[string]*rsa.PublicKey)
	seen := make(map[string]bool)
	for _, raw := range records {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return nil, run.ErrUnavailable
		}
		for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
			if _, exists := fields[private]; exists {
				return nil, run.ErrUnavailable
			}
		}
		var kind, kid, use, alg string
		if json.Unmarshal(fields["kty"], &kind) != nil || json.Unmarshal(fields["kid"], &kid) != nil || !identityText(kid, 256) || seen[kid] {
			return nil, run.ErrUnavailable
		}
		seen[kid] = true
		if json.Unmarshal(fields["use"], &use) != nil || json.Unmarshal(fields["alg"], &alg) != nil {
			return nil, run.ErrUnavailable
		}
		if kind != "RSA" || use != "sig" || alg != "RS256" {
			continue
		}
		if _, ok := jsondocument.ObjectFields(raw, []string{"kty", "kid", "use", "alg", "n", "e"}, "key_ops", "x5c", "x5t", "x5t#S256"); !ok {
			return nil, run.ErrUnavailable
		}
		if operations, exists := fields["key_ops"]; exists {
			var values []string
			if json.Unmarshal(operations, &values) != nil || len(values) != 1 || values[0] != "verify" {
				return nil, run.ErrUnavailable
			}
		}
		n, okN := identityJWKInteger(fields["n"], 1024)
		e, okE := identityJWKInteger(fields["e"], 4)
		if !okN || !okE || !e.IsInt64() || e.Int64() > 2147483647 {
			return nil, run.ErrUnavailable
		}
		key := &rsa.PublicKey{N: n, E: int(e.Int64())}
		if !validIdentityRSAKey(key) {
			return nil, run.ErrUnavailable
		}
		keys[kid] = key
	}
	if len(keys) == 0 {
		return nil, run.ErrUnavailable
	}
	return keys, nil
}
func identityJWKInteger(raw json.RawMessage, maximum int) (*big.Int, bool) {
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, false
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maximum || data[0] == 0 {
		return nil, false
	}
	return new(big.Int).SetBytes(data), true
}
