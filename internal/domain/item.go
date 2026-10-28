package domain

// Item represents an item record stored in PostgreSQL and cached in Redis.
type Item struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Price       int    `json:"price"`
	Description string `json:"description"`
}

// CacheResponse bundles an Item with its cache hit status.
type CacheResponse struct {
	Item     Item
	CacheHit bool
}
