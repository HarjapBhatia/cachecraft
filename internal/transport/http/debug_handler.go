// Package httptransport contains all HTTP routing and handler logic for CacheCraft.
//
// This file contains DebugHandler, which handles all /debug/* endpoints.
// These are operational/observability endpoints used by the test harness (run-test.sh)
// and the k6 scripts during load experiments.
//
// ENDPOINTS:
//   GET  /debug/db-queries       -- Returns SQL query count for this process.
//   POST /debug/cache/bulk-warm  -- Loads all 10,000 items into Redis in one pipeline.
//   POST /debug/cache/reset      -- Clears all item:* and lock:item:* keys (local only).
package httptransport

import (
	"errors"
	"net/http"
	"time"

	"cachecraft/internal/domain"
	"cachecraft/internal/service"
)

// DebugHandler handles all debug and administrative HTTP endpoints.
// It holds references to the repository (for query counting) and
// the cache (for bulk-warm and reset operations).
type DebugHandler struct {
	repo    service.ItemRepository
	cache   service.ItemCache
	resetter service.CacheResetter
}

// NewDebugHandler creates a new DebugHandler.
// resetter can be nil if running in non-local mode; the reset route is
// simply not registered in that case.
func NewDebugHandler(repo service.ItemRepository, cache service.ItemCache, resetter service.CacheResetter) *DebugHandler {
	return &DebugHandler{
		repo:     repo,
		cache:    cache,
		resetter: resetter,
	}
}

// GetDbQueryCount handles GET /debug/db-queries.
// Returns the number of SQL SELECT statements this API instance has executed
// since startup. The test harness calls this before and after a k6 run to
// compute the "query delta" that proves how effective each caching strategy is.
//
// Example response: { "dbQueryCount": 23 }
func (h *DebugHandler) GetDbQueryCount(w http.ResponseWriter, r *http.Request) {
	count := h.repo.QueryCount()
	writeJSON(w, http.StatusOK, domain.DbQueryCount{Count: count})
}

// BulkWarm handles POST /debug/cache/bulk-warm?ttl={seconds}.
// It loads every item in the database into Redis using a single pipeline batch,
// all expiring at the same absolute timestamp. This sets up the cache avalanche
// experiment where all 10,000 keys expire simultaneously.
//
// Example response: { "warmedKeyCount": 10000, "expiresAtEpochMs": 1729789200000 }
func (h *DebugHandler) BulkWarm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse ?ttl= (in seconds). Default to 30 seconds if absent or invalid.
	ttl := queryDuration(r, "ttl", 30*time.Second)

	items, err := h.repo.FindAll(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BULK_WARM_FAILED", "failed to load items from database")
		return
	}

	// Calculate the absolute expiry timestamp BEFORE the pipeline loop starts.
	// This ensures all keys expire at exactly the same millisecond, rather than
	// drifting slightly due to loop iteration time.
	expiresAt := time.Now().Add(ttl)
	expiresAtEpochMs := expiresAt.UnixMilli()

	if err := h.cache.PutAllWithAbsoluteExpiry(ctx, items, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, "BULK_WARM_FAILED", "failed to write items to cache")
		return
	}

	writeJSON(w, http.StatusOK, domain.BulkWarmResult{
		WarmedKeyCount:   len(items),
		ExpiresAtEpochMs: expiresAtEpochMs,
	})
}

// ResetCache handles POST /debug/cache/reset.
// Only registered in "local" mode by the router. Deletes all item and lock keys
// from Redis, ensuring a clean cache state before running a new experiment.
//
// Example response: { "deletedItemKeys": 10000, "deletedLockKeys": 0 }
func (h *DebugHandler) ResetCache(w http.ResponseWriter, r *http.Request) {
	if h.resetter == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "cache reset is not available in this environment")
		return
	}

	result, err := h.resetter.Reset(r.Context())
	if err != nil {
		if errors.Is(err, domain.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "RESET_FAILED", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "RESET_FAILED", "failed to reset cache")
		return
	}

	writeJSON(w, http.StatusOK, result)
}
