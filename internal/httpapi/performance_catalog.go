package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// PerformanceCatalogSource publishes safe immutable metadata for the authenticated principal.
// The read callback is already principal-scoped; cancellation/errors must be retained.
// Implementations must be concurrency-safe and return request-owned projection data.
type PerformanceCatalogSource interface {
	PerformanceCatalog(context.Context, string, func(context.Context, string, string) (baseline.Record, error)) (contract.CatalogProjection, error)
}

const catalogPageSize, catalogSnapshotLimit, catalogMaximumBytes = 100, 16, 1 << 20

type catalogSnapshot struct {
	principal string
	expires   time.Time
	pages     []json.RawMessage
}
type performanceCatalog struct {
	source    PerformanceCatalogSource
	now       func() time.Time
	mu        sync.Mutex
	snapshots map[string]catalogSnapshot
	building  int
}

func (h *Handler) servePerformanceCatalog(w http.ResponseWriter, r *http.Request, identity Identity) {
	if !identity.ReadCatalog {
		fail(w, 403, "FORBIDDEN")
		return
	}
	if h.catalog == nil {
		fail(w, 404, "NOT_FOUND")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 || len(query["cursor"]) > 1 {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	for key := range query {
		if key != "cursor" {
			fail(w, 400, "BAD_REQUEST")
			return
		}
	}
	cursor := query.Get("cursor")
	if len(cursor) > 256 || (cursor == "" && len(query) != 0) {
		fail(w, 400, "BAD_REQUEST")
		return
	}
	if cursor != "" {
		id, position, found := strings.Cut(cursor, ":")
		index, err := strconv.Atoi(position)
		h.catalog.mu.Lock()
		snapshot, exists := h.catalog.snapshots[id]
		h.catalog.mu.Unlock()
		if !found || err != nil || index < 1 || position != strconv.Itoa(index) {
			fail(w, 400, "BAD_REQUEST")
			return
		}
		if !exists || snapshot.principal != identity.Principal || !h.catalog.now().Before(snapshot.expires) || index >= len(snapshot.pages) {
			fail(w, 410, "SNAPSHOT_GONE")
			return
		}
		writeJSON(w, 200, snapshot.pages[index])
		return
	}
	h.catalog.mu.Lock()
	for id, snapshot := range h.catalog.snapshots {
		if !h.catalog.now().Before(snapshot.expires) {
			delete(h.catalog.snapshots, id)
		}
	}
	if len(h.catalog.snapshots)+h.catalog.building >= catalogSnapshotLimit {
		h.catalog.mu.Unlock()
		fail(w, 503, "UNAVAILABLE")
		return
	}
	h.catalog.building++
	h.catalog.mu.Unlock()
	defer func() { h.catalog.mu.Lock(); h.catalog.building--; h.catalog.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	projection, err := h.catalog.source.PerformanceCatalog(ctx, identity.Principal, func(ctx context.Context, id, version string) (baseline.Record, error) {
		return h.repository.GetBaseline(ctx, identity.Principal, id, version)
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		failRepository(w, err)
		return
	}
	pages, id, expires, err := h.catalog.materialize(projection, identity.Principal)
	if err != nil {
		fail(w, 503, "UNAVAILABLE")
		return
	}
	if err := ctx.Err(); err != nil {
		failRepository(w, err)
		return
	}
	h.catalog.mu.Lock()
	h.catalog.snapshots[id] = catalogSnapshot{principal: identity.Principal, expires: expires, pages: pages}
	h.catalog.mu.Unlock()
	writeJSON(w, 200, pages[0])
}

func (catalog *performanceCatalog) materialize(projection contract.CatalogProjection, principal string) ([]json.RawMessage, string, time.Time, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", time.Time{}, run.ErrUnavailable
	}
	id := hex.EncodeToString(nonce[:])
	now := catalog.now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Minute)
	encoded, err := json.Marshal(projection.Items)
	if err != nil || len(encoded) > catalogMaximumBytes || projection.Items == nil {
		return nil, "", expires, run.ErrUnavailable
	}
	digest, err := contract.CatalogDigest(projection.Items)
	if err != nil {
		return nil, "", expires, err
	}
	count := max(1, (len(projection.Items)+catalogPageSize-1)/catalogPageSize)
	metadata := map[string]any{
		"reference":   contract.CatalogReference{CatalogIdentity: contract.CatalogIdentity{Issuer: projection.Issuer, ID: id}, Revision: "1", SHA256: digest},
		"principal":   contract.CatalogIdentity{Issuer: projection.PrincipalIssuer, ID: principal},
		"producer":    map[string]any{"identity": contract.CatalogIdentity{Issuer: projection.Issuer, ID: "perfeng-control-plane"}, "version": projection.ProducerVersion},
		"generatedAt": now.Format(time.RFC3339Nano), "validUntil": expires.Format(time.RFC3339Nano), "complete": true, "totalItems": len(projection.Items), "totalPages": count,
	}
	pages := make([]json.RawMessage, 0, count)
	for index := range count {
		var cursor, next any
		if index > 0 {
			cursor = id + ":" + strconv.Itoa(index)
		}
		if index+1 < count {
			next = id + ":" + strconv.Itoa(index+1)
		}
		page := map[string]any{"schemaVersion": 1, "kind": "PerformanceCatalogPage", "snapshot": metadata, "page": map[string]any{"index": index, "cursor": cursor, "nextCursor": next}, "items": projection.Items[min(index*catalogPageSize, len(projection.Items)):min((index+1)*catalogPageSize, len(projection.Items))]}
		if contract.ValidateCatalogPage(page) != nil {
			return nil, "", expires, run.ErrUnavailable
		}
		data, err := json.Marshal(page)
		if err != nil {
			return nil, "", expires, err
		}
		pages = append(pages, data)
	}
	return pages, id, expires, nil
}
