// Package domain holds the core business entities of the CacheCraft application.
//
// This file defines the result types returned by debug and admin operations.
// These are simple data-carrying structs used as HTTP response bodies.
package domain

// DbQueryCount is the response body for GET /debug/db-queries.
// It exposes the count of SQL SELECT statements issued by this specific
// API process since it started. Used to measure the "query delta" across
// the 3-node cluster before and after a load test.
//
// Example JSON:
//   { "dbQueryCount": 182 }
type DbQueryCount struct {
	Count int64 `json:"dbQueryCount"`
}

// BulkWarmResult is the response body for POST /debug/cache/bulk-warm.
// It confirms how many keys were written to Redis and at what absolute
// expiry timestamp they will all expire simultaneously (the avalanche setup).
//
// Example JSON:
//   { "warmedKeyCount": 10000, "expiresAtEpochMs": 1729789200000 }
type BulkWarmResult struct {
	WarmedKeyCount int   `json:"warmedKeyCount"`
	ExpiresAtEpochMs int64 `json:"expiresAtEpochMs"`
}

// CacheResetResult is the response body for POST /debug/cache/reset.
// It reports how many Redis keys were deleted across item and lock namespaces.
//
// Example JSON:
//   { "deletedItemKeys": 10000, "deletedLockKeys": 0 }
type CacheResetResult struct {
	DeletedItemKeys int64 `json:"deletedItemKeys"`
	DeletedLockKeys int64 `json:"deletedLockKeys"`
}
