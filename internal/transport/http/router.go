package httptransport

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter builds and configures the Chi HTTP router with application routes and middleware.
func NewRouter(
	itemHandler *ItemHandler,
	debugHandler *DebugHandler,
	isLocal bool,
) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Use(middleware.RequestID)

	r.Get("/api/items/{id}", itemHandler.GetItem)
	r.Get("/debug/db-queries", debugHandler.GetDbQueryCount)
	r.Post("/debug/cache/bulk-warm", debugHandler.BulkWarm)

	if isLocal {
		r.Post("/debug/cache/reset", debugHandler.ResetCache)
	}

	return r
}
