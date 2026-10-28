package httptransport

import (
	"errors"
	"net/http"
	"time"

	"cachecraft/internal/domain"
	"cachecraft/internal/service"
)

// DebugHandler handles operational and test observability endpoints.
type DebugHandler struct {
	repo     service.ItemRepository
	cache    service.ItemCache
	resetter service.CacheResetter
}

// NewDebugHandler constructs a new DebugHandler instance.
func NewDebugHandler(repo service.ItemRepository, cache service.ItemCache, resetter service.CacheResetter) *DebugHandler {
	return &DebugHandler{
		repo:     repo,
		cache:    cache,
		resetter: resetter,
	}
}

// GetDbQueryCount handles GET /debug/db-queries.
func (h *DebugHandler) GetDbQueryCount(w http.ResponseWriter, r *http.Request) {
	count := h.repo.QueryCount()
	writeJSON(w, http.StatusOK, domain.DbQueryCount{Count: count})
}

// BulkWarm handles POST /debug/cache/bulk-warm?ttl={seconds}.
func (h *DebugHandler) BulkWarm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ttl := queryDuration(r, "ttl", 30*time.Second)

	items, err := h.repo.FindAll(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "BULK_WARM_FAILED", "failed to load items from database")
		return
	}

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
