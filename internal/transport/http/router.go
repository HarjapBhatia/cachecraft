// Package httptransport contains all HTTP routing and handler logic for CacheCraft.
//
// This file sets up the Chi router and defines all routes.
//
// DEPENDENCY: github.com/go-chi/chi/v5
//   Chi is a lightweight HTTP router for Go built on top of the standard
//   library's net/http package. It adds:
//     - URL parameter extraction: chi.URLParam(r, "id") instead of parsing manually.
//     - Middleware chaining: r.Use(middleware.Logger).
//     - Route grouping: r.Route("/api", func(r chi.Router) { ... }).
//
//   Chi is a "mux" (multiplexer) -- it routes incoming requests to the correct
//   handler function based on the HTTP method and URL pattern.
//
// HOW Go HTTP works:
//   An HTTP handler in Go is any type that implements:
//     ServeHTTP(w http.ResponseWriter, r *http.Request)
//   Chi's router implements this interface, so you can pass chi.Router directly
//   to http.ListenAndServe as the handler.
//
//   http.ResponseWriter: the object you write your response into (status code, headers, body).
//   *http.Request:       the incoming request (URL, headers, body, context).
//
// The "isLocal" flag mirrors Spring Boot's @Profile("local") guard.
// When true, the destructive /debug/cache/reset endpoint is registered.
package httptransport

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter creates and configures the Chi router with all application routes.
// The concrete handler structs are passed in so the router has no knowledge of
// their internal implementation details (only their methods).
func NewRouter(
	itemHandler *ItemHandler,
	debugHandler *DebugHandler,
	isLocal bool,
) http.Handler {
	r := chi.NewRouter()

	// Middleware is applied in the order it is added. Each middleware wraps the
	// next handler in the chain, forming a pipeline:
	// Request -> Recoverer -> Logger -> RequestID -> [actual handler] -> Response

	// middleware.Recoverer catches any panic() inside a handler and converts it
	// to an HTTP 500 response, preventing the whole server from crashing.
	r.Use(middleware.Recoverer)

	// middleware.Logger logs each request: method, path, status, latency.
	// Output goes to stdout. In a real deployment you would swap this for
	// a structured JSON logger.
	r.Use(middleware.Logger)

	// middleware.RequestID attaches a unique ID to each request context.
	// Useful for tracing a request through log lines.
	r.Use(middleware.RequestID)

	// Item API routes
	r.Get("/api/items/{id}", itemHandler.GetItem)

	// Debug routes: always available
	r.Get("/debug/db-queries", debugHandler.GetDbQueryCount)
	r.Post("/debug/cache/bulk-warm", debugHandler.BulkWarm)

	// Destructive reset: only in local/dev mode.
	// This mirrors the @Profile("local") annotation in the Spring Boot version.
	if isLocal {
		r.Post("/debug/cache/reset", debugHandler.ResetCache)
	}

	return r
}
