// Package domain holds the core business entities of the CacheCraft application.
//
// In Go, a "domain" package contains the raw data types that represent your
// business concepts. These types have NO knowledge of how they are stored (SQL),
// cached (Redis), or transported (HTTP). They are plain data structures.
//
// Think of this package like your Java "model" package, but even leaner:
// no annotations, no framework code, just pure data types.
package domain

// Item represents a single row in the "items" PostgreSQL table.
//
// In Go, we use "structs" instead of Java classes or Python dataclasses.
// Each field has three parts:
//   - The field name (must start with uppercase to be "exported", i.e. public)
//   - The field type
//   - A "struct tag" (the backtick string) telling the JSON encoder what key name to use
//
// Example JSON output for this struct:
//
//	{ "id": 42, "name": "Item 42", "price": 1654, "description": "..." }
type Item struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	Description string `json:"description"`
}

// CacheResponse bundles an Item with a boolean indicating whether the item
// came from the Redis cache (true) or was freshly loaded from PostgreSQL (false).
//
// The HTTP handler uses this to decide what to put in the "X-Cache" response header.
// "HIT" means it came from the cache, "MISS" means the database was queried.
type CacheResponse struct {
	Item     Item
	CacheHit bool
}
