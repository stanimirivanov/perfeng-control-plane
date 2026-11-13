package authentication

import (
	"context"
	"crypto/rsa"
	"math/big"
	"sync"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// IdentityKeyFetcher must return independently owned, validated snapshots from
// one admitted issuer/origin and honor context cancellation. Fetching is bounded
// to three seconds by the cache as well as by the concrete HTTPS fetcher.
type IdentityKeyFetcher interface {
	FetchIdentityKeys(context.Context) (IdentityKeySnapshot, error)
}

// IdentityKeyCache coalesces refreshes for one admitted issuer/origin per process.
// It retains at most 32 keys and no attacker-supplied negative IDs. Use one cache
// instance per issuer, not per request; its ten-second rate bound is not fleet-wide.
type IdentityKeyCache struct {
	mu              sync.Mutex
	fetcher         IdentityKeyFetcher
	now             func() time.Time
	ctx             context.Context
	cancel          context.CancelFunc
	snapshot        IdentityKeySnapshot
	next, observed  time.Time
	flight          chan struct{}
	failed, stopped bool
}

// NewIdentityKeyCache performs no I/O. The fetcher and clock must support
// concurrent calls. The owner must Stop the cache and separately retire the
// borrowed fetcher's idle transport connections at shutdown.
func NewIdentityKeyCache(fetcher IdentityKeyFetcher, now func() time.Time) (*IdentityKeyCache, error) {
	if fetcher == nil || now == nil {
		return nil, run.ErrValidation
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &IdentityKeyCache{fetcher: fetcher, now: now, ctx: ctx, cancel: cancel}, nil
}

// Stop prevents further trust reads and cancels shared work. It is idempotent;
// cooperative fetchers terminate within the shared three-second request budget.
func (cache *IdentityKeyCache) Stop() {
	cache.mu.Lock()
	cache.stopped = true
	cache.mu.Unlock()
	cache.cancel()
}

// LookupIdentityKey returns an owned fresh key without renewing its timestamp.
// Missing keys share one bounded refresh when the ten-second attempt interval
// allows it; each caller waits at most once and can cancel independently. Recent
// successful snapshot absence is negative-cached without retaining requested IDs.
func (cache *IdentityKeyCache) LookupIdentityKey(ctx context.Context, kid string) (IdentitySigningKey, error) {
	if err := ctx.Err(); err != nil {
		return IdentitySigningKey{}, err
	}
	if !identityText(kid, 256) {
		return IdentitySigningKey{}, ErrUnauthenticated
	}
	cache.mu.Lock()
	if cache.stopped {
		cache.mu.Unlock()
		return IdentitySigningKey{}, run.ErrUnavailable
	}
	now := cache.now()
	if !cache.observeTimeLocked(now) {
		cache.mu.Unlock()
		return IdentitySigningKey{}, run.ErrUnavailable
	}
	if key, exists := cache.snapshot.Keys[kid]; exists && freshIdentityKey(IdentitySigningKey{PublicKey: key, FetchedAt: cache.snapshot.FetchedAt}, now) {
		value := IdentitySigningKey{PublicKey: copyIdentityRSAKey(key), FetchedAt: cache.snapshot.FetchedAt}
		cache.mu.Unlock()
		return value, nil
	}
	if cache.flight == nil && now.Before(cache.next) {
		value, err := cache.resultLocked(kid, now)
		cache.mu.Unlock()
		return value, err
	}
	if cache.flight == nil {
		cache.flight = make(chan struct{})
		cache.next = now.Add(10 * time.Second)
		go cache.refresh(cache.flight)
	}
	done := cache.flight
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return IdentitySigningKey{}, ctx.Err()
	case <-done:
	}
	if err := ctx.Err(); err != nil {
		return IdentitySigningKey{}, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.resultLocked(kid, cache.now())
}

func (cache *IdentityKeyCache) resultLocked(kid string, now time.Time) (IdentitySigningKey, error) {
	if cache.stopped || !cache.observeTimeLocked(now) {
		return IdentitySigningKey{}, run.ErrUnavailable
	}
	if key, exists := cache.snapshot.Keys[kid]; exists && freshIdentityKey(IdentitySigningKey{PublicKey: key, FetchedAt: cache.snapshot.FetchedAt}, now) {
		return IdentitySigningKey{PublicKey: copyIdentityRSAKey(key), FetchedAt: cache.snapshot.FetchedAt}, nil
	}
	if cache.failed || !freshIdentitySnapshotTime(cache.snapshot.FetchedAt, now) {
		return IdentitySigningKey{}, run.ErrUnavailable
	}
	return IdentitySigningKey{}, ErrUnauthenticated
}

func (cache *IdentityKeyCache) refresh(done chan struct{}) {
	ctx, cancel := context.WithTimeout(cache.ctx, 3*time.Second)
	defer cancel()
	snapshot, err := cache.fetcher.FetchIdentityKeys(ctx)
	if err == nil {
		snapshot, err = ownIdentitySnapshot(snapshot, cache.now())
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	completed := cache.now()
	if !cache.observeTimeLocked(completed) || ctx.Err() != nil || cache.stopped || !freshIdentitySnapshotTime(snapshot.FetchedAt, completed) || snapshot.FetchedAt.Before(cache.snapshot.FetchedAt) {
		err = run.ErrUnavailable
	}
	cache.failed = err != nil
	if err == nil {
		cache.snapshot = snapshot
	}
	cache.flight = nil
	close(done)
}

func ownIdentitySnapshot(snapshot IdentityKeySnapshot, now time.Time) (IdentityKeySnapshot, error) {
	if len(snapshot.Keys) == 0 || len(snapshot.Keys) > 32 || !freshIdentitySnapshotTime(snapshot.FetchedAt, now) {
		return IdentityKeySnapshot{}, run.ErrUnavailable
	}
	owned := IdentityKeySnapshot{Keys: make(map[string]*rsa.PublicKey, len(snapshot.Keys)), FetchedAt: snapshot.FetchedAt}
	for kid, key := range snapshot.Keys {
		if !identityText(kid, 256) || !validIdentityRSAKey(key) {
			return IdentityKeySnapshot{}, run.ErrUnavailable
		}
		owned.Keys[kid] = copyIdentityRSAKey(key)
	}
	return owned, nil
}
func freshIdentitySnapshotTime(fetched, now time.Time) bool {
	return !fetched.IsZero() && !fetched.After(now) && now.Sub(fetched) <= 5*time.Minute
}
func copyIdentityRSAKey(key *rsa.PublicKey) *rsa.PublicKey {
	return &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
}

// Refusing backwards time prevents a previously expired snapshot becoming fresh.
func (cache *IdentityKeyCache) observeTimeLocked(now time.Time) bool {
	if now.IsZero() || now.Before(cache.observed) {
		return false
	}
	cache.observed = now
	return true
}
