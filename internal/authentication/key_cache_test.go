package authentication

import (
	"context"
	"crypto/rsa"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type identityKeyFetchFunc func(context.Context) (IdentityKeySnapshot, error)

func (f identityKeyFetchFunc) FetchIdentityKeys(ctx context.Context) (IdentityKeySnapshot, error) {
	return f(ctx)
}

func TestIdentityKeyCacheRotationBoundsAndOutage(t *testing.T) {
	key, _ := jwksFixture(t)
	rotated, _ := jwksFixture(t)
	profile, claims, now := identityFixture()
	calls := 0
	var fetchErr error
	snapshot := IdentityKeySnapshot{Keys: map[string]*rsa.PublicKey{"current": &key.PublicKey}, FetchedAt: now}
	cache, err := NewIdentityKeyCache(identityKeyFetchFunc(func(context.Context) (IdentityKeySnapshot, error) { calls++; return snapshot, fetchErr }), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Stop()
	first, err := cache.LookupIdentityKey(context.Background(), "current")
	if err != nil {
		t.Fatal(err)
	}
	first.PublicKey.N.SetInt64(1)
	again, err := cache.LookupIdentityKey(context.Background(), "current")
	if err != nil || again.PublicKey.N.Cmp(key.N) != 0 || calls != 1 {
		t.Fatal("cache aliases or refetches fresh keys", err, calls)
	}
	for i := range 100 {
		if _, err = cache.LookupIdentityKey(context.Background(), "unknown-"+strconv.Itoa(i)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("unknown-key burst escaped rate bound", calls)
	}
	now = now.Add(11 * time.Second)
	rotatedAt := now
	snapshot = IdentityKeySnapshot{Keys: map[string]*rsa.PublicKey{"next": &rotated.PublicKey}, FetchedAt: now}
	if _, err = cache.LookupIdentityKey(context.Background(), "next"); err != nil || calls != 2 {
		t.Fatal("rotation not fetched", err, calls)
	}
	if _, err = cache.LookupIdentityKey(context.Background(), "current"); !errors.Is(err, ErrUnauthenticated) || calls != 2 {
		t.Fatal("retired key retained", err, calls)
	}
	verifier, err := NewJWTIdentityVerifier(profile.Issuer, profile.Audience, cache, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	newToken := signIdentityJWT(t, rotated, `{"alg":"RS256","typ":"JWT","kid":"next"}`, jwtPayload(t, claims))
	oldToken := signIdentityJWT(t, key, `{"alg":"RS256","typ":"JWT","kid":"current"}`, jwtPayload(t, claims))
	if _, err = verifier.VerifyIdentityCredential(context.Background(), newToken); err != nil {
		t.Fatal("rotated signature refused", err)
	}
	if _, err = verifier.VerifyIdentityCredential(context.Background(), oldToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("retired signature accepted", err)
	}

	now = now.Add(11 * time.Second)
	fetchErr = errors.New("secret backend detail")
	if _, err = cache.LookupIdentityKey(context.Background(), "unknown"); err != run.ErrUnavailable {
		t.Fatal("refresh outage hidden or leaked", err)
	}
	fresh, err := cache.LookupIdentityKey(context.Background(), "next")
	if err != nil || !fresh.FetchedAt.Equal(rotatedAt) || calls != 3 {
		t.Fatal("outage renewed or lost fresh trust", err, calls)
	}
	now = now.Add(5 * time.Minute)
	if value, err := cache.LookupIdentityKey(context.Background(), "next"); err != run.ErrUnavailable || value.PublicKey != nil || calls != 4 {
		t.Fatal("stale key served during outage", err, calls)
	}
	now = rotatedAt.Add(time.Minute)
	if value, err := cache.LookupIdentityKey(context.Background(), "next"); err != run.ErrUnavailable || value.PublicKey != nil || calls != 4 {
		t.Fatal("clock rollback resurrected stale trust", err, calls)
	}
}

func TestIdentityKeyCacheSharesConcurrentRefresh(t *testing.T) {
	key, _ := jwksFixture(t)
	_, _, now := identityFixture()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache, err := NewIdentityKeyCache(identityKeyFetchFunc(func(ctx context.Context) (IdentityKeySnapshot, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return IdentityKeySnapshot{Keys: map[string]*rsa.PublicKey{"current": &key.PublicKey}, FetchedAt: now}, nil
		case <-ctx.Done():
			return IdentityKeySnapshot{}, ctx.Err()
		}
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Stop()
	done := make(chan error, 8)
	var entered sync.WaitGroup
	entered.Add(8)
	for range 8 {
		go func() {
			entered.Done()
			_, err := cache.LookupIdentityKey(context.Background(), "current")
			done <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch not started")
	}
	entered.Wait()
	close(release)
	for range 8 {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("waiter hung")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("refresh not coalesced", calls.Load())
	}
}

func TestIdentityKeyCacheCancelledInitiatorDoesNotCancelSharedWork(t *testing.T) {
	key, _ := jwksFixture(t)
	_, _, now := identityFixture()
	started := make(chan struct{})
	release := make(chan struct{})
	cache, err := NewIdentityKeyCache(identityKeyFetchFunc(func(ctx context.Context) (IdentityKeySnapshot, error) {
		close(started)
		select {
		case <-release:
			return IdentityKeySnapshot{Keys: map[string]*rsa.PublicKey{"current": &key.PublicKey}, FetchedAt: now}, nil
		case <-ctx.Done():
			return IdentityKeySnapshot{}, ctx.Err()
		}
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cache.LookupIdentityKey(ctx, "current"); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller hung")
	}
	close(release)
	if _, err = cache.LookupIdentityKey(context.Background(), "current"); err != nil {
		t.Fatal("initiator cancelled shared refresh", err)
	}
	cache.Stop()
	if _, err = cache.LookupIdentityKey(context.Background(), "current"); err != run.ErrUnavailable {
		t.Fatal("stopped cache served trust", err)
	}
}

func TestIdentityKeyCacheRefusesInvalidSnapshots(t *testing.T) {
	key, _ := jwksFixture(t)
	for _, mode := range []string{"empty", "future", "stale", "nil-key", "oversized", "clock-reversal"} {
		t.Run(mode, func(t *testing.T) {
			_, _, now := identityFixture()
			snapshot := IdentityKeySnapshot{Keys: map[string]*rsa.PublicKey{"current": &key.PublicKey}, FetchedAt: now}
			switch mode {
			case "empty":
				snapshot.Keys = nil
			case "future":
				snapshot.FetchedAt = now.Add(time.Second)
			case "stale":
				snapshot.FetchedAt = now.Add(-6 * time.Minute)
			case "nil-key":
				snapshot.Keys["current"] = nil
			case "oversized":
				for i := range 33 {
					snapshot.Keys[strconv.Itoa(i)] = &key.PublicKey
				}
			}
			cache, err := NewIdentityKeyCache(identityKeyFetchFunc(func(context.Context) (IdentityKeySnapshot, error) { return snapshot, nil }), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Stop()
			if mode == "clock-reversal" {
				if _, err = cache.LookupIdentityKey(context.Background(), "current"); err != nil {
					t.Fatal(err)
				}
				now = now.Add(-time.Minute)
			}
			if value, err := cache.LookupIdentityKey(context.Background(), "current"); err != run.ErrUnavailable || value.PublicKey != nil {
				t.Fatal("invalid snapshot served", err)
			}
		})
	}
}

func TestIdentityKeyCacheStopCancelsInflightRefresh(t *testing.T) {
	_, _, now := identityFixture()
	started := make(chan struct{})
	cache, err := NewIdentityKeyCache(identityKeyFetchFunc(func(ctx context.Context) (IdentityKeySnapshot, error) {
		close(started)
		<-ctx.Done()
		return IdentityKeySnapshot{}, ctx.Err()
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Stop()
	done := make(chan error, 1)
	go func() { _, err := cache.LookupIdentityKey(context.Background(), "current"); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start")
	}
	cache.Stop()
	select {
	case err := <-done:
		if err != run.ErrUnavailable {
			t.Fatal("stopped refresh exposed trust or internals", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown left waiter blocked")
	}
}
