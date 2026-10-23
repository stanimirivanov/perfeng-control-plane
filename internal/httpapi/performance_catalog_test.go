package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

type catalogSourceFunc func(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error)

func (source catalogSourceFunc) PerformanceCatalog(ctx context.Context, principal string, read func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
	return source(ctx, principal, read)
}

func catalogHandler(t *testing.T) (*Handler, *time.Time) {
	t.Helper()
	h, _ := setup(t)
	authenticate := h.authenticate
	h.authenticate = func(ctx context.Context, token string) (Identity, error) {
		identity, err := authenticate(ctx, token)
		identity.ReadCatalog = token != "read-only"
		return identity, err
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	h.catalog = &performanceCatalog{now: func() time.Time { return now }, snapshots: make(map[string]catalogSnapshot), source: catalogSourceFunc(func(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
		items := make([]map[string]any, 201)
		for index := range items {
			items[index] = map[string]any{"kind": "environment", "state": "ACTIVE", "displayName": "environment", "reference": contract.CatalogReference{CatalogIdentity: contract.CatalogIdentity{Issuer: "https://provider.example/identity", ID: "env-" + strconv.Itoa(index)}, Revision: "1", SHA256: strings.Repeat("a", 64)}}
		}
		return contract.CatalogProjection{Issuer: "https://provider.example/identity", PrincipalIssuer: "https://auth.example/tenant", ProducerVersion: "1.0.0", Items: items}, nil
	})}
	return h, &now
}

func catalogGet(h *Handler, token, cursor string) *httptest.ResponseRecorder {
	path := "/v1/performance-catalog"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	r := httptest.NewRequest("GET", path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func catalogPage(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var page map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateCatalogPage(page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestPerformanceCatalogPaginationAuthorityExpiryAndConcurrency(t *testing.T) {
	h, now := catalogHandler(t)
	first := catalogPage(t, catalogGet(h, "alice-token", ""))
	h.catalog.source = catalogSourceFunc(func(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
		t.Fatal("cursor reread the mutable source")
		return contract.CatalogProjection{}, errors.New("unexpected source read")
	})
	cursor := first["page"].(map[string]any)["nextCursor"].(string)
	for token, status := range map[string]int{"": 401, "read-only": 403, "bob-token": 410} {
		if w := catalogGet(h, token, cursor); w.Code != status {
			t.Fatalf("%s: status %d", token, w.Code)
		}
	}
	second := catalogPage(t, catalogGet(h, "rotated-token", cursor))
	a, _ := json.Marshal(first["snapshot"])
	b, _ := json.Marshal(second["snapshot"])
	if string(a) != string(b) {
		t.Fatal("mixed snapshot")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if w := catalogGet(h, "alice-token", cursor); w.Code != 200 {
				t.Errorf("concurrent status %d", w.Code)
			}
		})
	}
	group.Wait()
	last := catalogPage(t, catalogGet(h, "alice-token", second["page"].(map[string]any)["nextCursor"].(string)))
	if len(last["items"].([]any)) != 1 || last["page"].(map[string]any)["nextCursor"] != nil {
		t.Fatal("incomplete final page")
	}
	items := append(first["items"].([]any), second["items"].([]any)...)
	items = append(items, last["items"].([]any)...)
	digest, err := contract.CatalogDigest(items)
	if err != nil || digest != first["snapshot"].(map[string]any)["reference"].(map[string]any)["sha256"] {
		t.Fatalf("snapshot digest mismatch: %s, %v", digest, err)
	}
	*now = now.Add(time.Minute)
	if w := catalogGet(h, "alice-token", cursor); w.Code != 410 {
		t.Fatalf("expired status %d", w.Code)
	}
}

func TestPerformanceCatalogCapacityRedactionAndInvalidQueries(t *testing.T) {
	h, now := catalogHandler(t)
	for range catalogSnapshotLimit {
		catalogPage(t, catalogGet(h, "alice-token", ""))
	}
	if w := catalogGet(h, "alice-token", ""); w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal("unbounded snapshot admission")
	}
	*now = now.Add(time.Minute)
	catalogPage(t, catalogGet(h, "alice-token", ""))
	h.catalog.source = catalogSourceFunc(func(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
		return contract.CatalogProjection{}, errors.New("secret backend DSN")
	})
	if w := catalogGet(h, "alice-token", ""); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("backend failure leaked")
	}
	for _, query := range []string{"?principal=bob", "?cursor=", "?cursor=x&cursor=y", "?cursor=%zz"} {
		r := httptest.NewRequest("GET", "/v1/performance-catalog"+query, nil)
		r.Header.Set("Authorization", "Bearer alice-token")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("query %s: %d", query, w.Code)
		}
	}
	h.catalog = nil
	if w := catalogGet(h, "alice-token", ""); w.Code != 404 {
		t.Fatal("disabled catalog exposed")
	}
}

func TestPerformanceCatalogDoesNotPublishAfterCancellation(t *testing.T) {
	h, _ := catalogHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := h.catalog.source
	h.catalog.source = catalogSourceFunc(func(ctx context.Context, principal string, read func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
		projection, err := source.PerformanceCatalog(ctx, principal, read)
		cancel()
		return projection, err
	})
	request := httptest.NewRequest("GET", "/v1/performance-catalog", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer alice-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 503 || len(h.catalog.snapshots) != 0 || h.catalog.building != 0 {
		t.Fatalf("cancelled publication: status=%d snapshots=%d building=%d", response.Code, len(h.catalog.snapshots), h.catalog.building)
	}
}

func TestPerformanceCatalogBoundsConcurrentMaterialization(t *testing.T) {
	h, _ := catalogHandler(t)
	source := h.catalog.source
	started := make(chan struct{}, catalogSnapshotLimit)
	release := make(chan struct{})
	completed := make(chan int, catalogSnapshotLimit)
	var calls atomic.Int32
	h.catalog.source = catalogSourceFunc(func(ctx context.Context, principal string, read func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error) {
		if calls.Add(1) > int32(catalogSnapshotLimit) {
			return contract.CatalogProjection{}, errors.New("source capacity exceeded")
		}
		started <- struct{}{}
		select {
		case <-release:
			projection, err := source.PerformanceCatalog(ctx, principal, read)
			projection.Items = projection.Items[:0]
			return projection, err
		case <-ctx.Done():
			return contract.CatalogProjection{}, ctx.Err()
		}
	})
	var group sync.WaitGroup
	defer group.Wait()
	defer close(release)
	for range catalogSnapshotLimit {
		group.Go(func() {
			status := catalogGet(h, "alice-token", "").Code
			if status != 200 {
				t.Errorf("admitted materialization returned %d", status)
			}
			completed <- status
		})
	}
	for range catalogSnapshotLimit {
		select {
		case <-started:
		case status := <-completed:
			t.Fatalf("materialization exited before release: %d", status)
		}
	}
	if w := catalogGet(h, "alice-token", ""); w.Code != 503 || calls.Load() != int32(catalogSnapshotLimit) {
		t.Fatalf("unbounded concurrent materialization: status=%d source calls=%d", w.Code, calls.Load())
	}
}
