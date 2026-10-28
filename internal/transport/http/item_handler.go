// Package httptransport contains all HTTP routing and handler logic for CacheCraft.
//
// This file contains ItemHandler, which handles GET /api/items/{id}.
//
// HANDLER STRUCTURE in Go:
//
//	A handler is a method with this exact signature:
//	  func (h *ItemHandler) SomeMethod(w http.ResponseWriter, r *http.Request)
//	"w" is what you write the response into.
//	"r" is the incoming request (URL, headers, body, and context).
//
// QUERY PARAMETER PARSING:
//
//	r.URL.Query().Get("strategy") returns the value of ?strategy=...
//	If the parameter is absent, it returns an empty string "".
//	We apply default values by checking for empty strings.
//
// URL PATH PARAMETER:
//
//	chi.URLParam(r, "id") extracts the {id} segment from the URL path.
//	For /api/items/42, chi.URLParam(r, "id") returns "42".
//	We then use strconv.ParseInt to convert it to int64.
//
// ERROR MAPPING:
//
//	Instead of a global @ExceptionHandler (Spring Boot style), each handler
//	explicitly maps errors to HTTP responses. This is more transparent and
//	lets you see exactly which errors produce which status codes in one place.
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

// ItemHandler handles all item-related HTTP endpoints.
type ItemHandler struct {
	svc           *service.ItemService
	defaultTTL    time.Duration
	defaultJitter float64
}

// NewItemHandler creates a new ItemHandler.
// The default TTL and jitter are passed in from the config so the handler
// knows what to use when the caller omits the ?ttl= or ?jitter= query params.
func NewItemHandler(svc *service.ItemService, defaultTTL time.Duration, defaultJitter float64) *ItemHandler {
	return &ItemHandler{
		svc:           svc,
		defaultTTL:    defaultTTL,
		defaultJitter: defaultJitter,
	}
}

// GetItem handles GET /api/items/{id}.
//
// Query parameters:
//   strategy -- which caching strategy to use (default: "no-cache")
//   ttl      -- cache TTL in seconds (default: from config, typically 30)
//   delay    -- artificial pg_sleep delay in milliseconds (default: 0)
//   jitter   -- symmetric TTL jitter fraction 0.0 to 1.0 (default: from config, typically 0.10)
//
// Response headers:
//   X-Cache: HIT  -- item was served from Redis
//   X-Cache: MISS -- item was loaded from PostgreSQL
//
// Error responses:
//   400 -- invalid parameter values or unsupported strategy
//   404 -- item ID does not exist in the database
//   503 -- database connection pool exhausted (simulated saturation event)
func (h *ItemHandler) GetItem(w http.ResponseWriter, r *http.Request) {
	// Parse and validate the {id} path parameter.
	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "id must be a positive integer")
		return
	}

	// Parse query parameters with defaults.
	strategy := queryString(r, "strategy", "no-cache")
	ttl := queryDuration(r, "ttl", h.defaultTTL)
	delay := queryInt64(r, "delay", 0)
	jitter := queryFloat64(r, "jitter", h.defaultJitter)

	// Validate delay (must be non-negative)
	if delay < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "delay must be >= 0")
		return
	}

	// Validate jitter range [0.0, 1.0]
	if jitter < 0.0 || jitter > 1.0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "jitter must be between 0.0 and 1.0")
		return
	}

	// Derive a child context from the request context with the database pool
	// timeout as a deadline. When the deadline fires, any in-flight database
	// query returns context.DeadlineExceeded, which we map to HTTP 503.
	//
	// r.Context() is the request's built-in context. It is cancelled automatically
	// by the HTTP server when the client disconnects.
	//
	// The pool timeout we apply here is separate: 2 seconds is how long we let
	// the goroutine wait for a pool connection before declaring pool exhaustion.
	//
	// context.WithTimeout returns (ctx, cancelFunc). The cancel function MUST
	// be called to release resources (Go's memory model requires this).
	// "defer cancel()" ensures it runs when GetItem returns.
	poolTimeout := 2 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), poolTimeout)
	defer cancel()

	resp, err := h.svc.GetItem(ctx, id, strategy, ttl, delay, jitter)
	if err != nil {
		h.mapError(w, err)
		return
	}

	// Set the X-Cache header BEFORE writing the status code (order matters in Go).
	if resp.CacheHit {
		w.Header().Set("X-Cache", "HIT")
	} else {
		w.Header().Set("X-Cache", "MISS")
	}

	writeJSON(w, http.StatusOK, resp.Item)
}

// mapError converts domain errors to their appropriate HTTP status codes and
// structured JSON error bodies. This is the Go equivalent of Spring Boot's
// @ExceptionHandler methods inside @RestControllerAdvice.
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
		// This is the pool exhaustion case: context timed out while waiting for
		// a database connection. Maps to HTTP 503, same as the Java version's
		// CannotGetJdbcConnectionException handler.
		writeError(w, http.StatusServiceUnavailable, "DATABASE_CONNECTION_TIMEOUT", "database connection pool is unavailable")

	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "an unexpected error occurred")
	}
}

// queryString reads a string query parameter with a fallback default.
func queryString(r *http.Request, key, defaultVal string) string {
	if v := r.URL.Query().Get(key); v != "" {
		return v
	}
	return defaultVal
}

// queryInt64 reads an integer query parameter. Returns defaultVal on parse failure or absence.
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

// queryFloat64 reads a float query parameter. Returns defaultVal on parse failure or absence.
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

// queryDuration reads a "seconds" integer query parameter and converts it to a
// time.Duration. Returns defaultVal if the parameter is absent or invalid.
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
