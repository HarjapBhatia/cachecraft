package httptransport

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cachecraft/internal/domain"
	"cachecraft/internal/service"

	"github.com/go-chi/chi/v5"
)

// ItemHandler handles item retrieval endpoints.
type ItemHandler struct {
	svc           *service.ItemService
	defaultTTL    time.Duration
	defaultJitter float64
}

// NewItemHandler constructs a new ItemHandler.
func NewItemHandler(svc *service.ItemService, defaultTTL time.Duration, defaultJitter float64) *ItemHandler {
	return &ItemHandler{
		svc:           svc,
		defaultTTL:    defaultTTL,
		defaultJitter: defaultJitter,
	}
}

// GetItem handles GET /api/items/{id}.
func (h *ItemHandler) GetItem(w http.ResponseWriter, r *http.Request) {
	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "id must be a positive integer")
		return
	}

	strategy := queryString(r, "strategy", "no-cache")
	ttl := queryDuration(r, "ttl", h.defaultTTL)
	delay := queryInt64(r, "delay", 0)
	jitter := queryFloat64(r, "jitter", h.defaultJitter)

	if delay < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "delay must be >= 0")
		return
	}

	if jitter < 0.0 || jitter > 1.0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "jitter must be between 0.0 and 1.0")
		return
	}

	poolTimeout := 2 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), poolTimeout)
	defer cancel()

	resp, err := h.svc.GetItem(ctx, id, strategy, ttl, delay, jitter)
	if err != nil {
		h.mapError(w, err)
		return
	}

	if resp.CacheHit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}

	writeJSON(w, http.StatusOK, resp.Item)
}

func (h *ItemHandler) mapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "ITEM_NOT_FOUND", err.Error())

	case errors.Is(err, domain.ErrUnsupportedStrategy):
		writeError(w, http.StatusBadRequest, "UNSUPPORTED_STRATEGY", err.Error())

	case errors.Is(err, domain.ErrInvalidLockDelay):
		writeError(w, http.StatusBadRequest, "INVALID_DISTRIBUTED_LOCK_DELAY", err.Error())

	case errors.Is(err, domain.ErrLockUnavailable):
		writeError(w, http.StatusServiceUnavailable, "CACHE_LOCK_UNAVAILABLE", err.Error())

	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "DATABASE_CONNECTION_TIMEOUT", "database connection pool is unavailable")

	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "an unexpected error occurred")
	}
}

func queryString(r *http.Request, key, defaultVal string) string {
	if v := r.URL.Query().Get(key); v != "" {
		return v
	}
	return defaultVal
}

func queryInt64(r *http.Request, key string, defaultVal int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

func queryFloat64(r *http.Request, key string, defaultVal float64) float64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return f
}

func queryDuration(r *http.Request, key string, defaultVal time.Duration) time.Duration {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return defaultVal
	}
	return time.Duration(n) * time.Second
}
