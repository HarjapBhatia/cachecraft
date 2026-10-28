// Package service contains the core application logic (what Spring Boot calls
// the "Service" layer). It coordinates caching strategies, but it does NOT
// directly call PostgreSQL or Redis. Instead, it talks to interfaces defined
// in this file.
//
// This file defines the "ports" -- the interface contracts that the service
// layer depends on. The actual implementations live in other packages
// (repository, cache, lock) and satisfy these interfaces.
//
// WHY interfaces?
//   This is the Dependency Inversion Principle (the "D" in SOLID).
//   ItemService does not know whether the cache is backed by Redis, Memcached,
//   or an in-memory map. It only knows "I can call Get and Put". This makes:
//   1. Unit testing easy: swap real Redis for a fake in-memory struct.
//   2. Refactoring safe: change the Redis client without touching the service.
//
// In Go, interfaces are "implicitly satisfied": a type satisfies an interface
// if it has all the required methods. There is no "implements" keyword like Java.
// This is called "structural typing" or "duck typing".
//
// Example:
//   If RedisItemCache has methods Get, Put, PutAllWithAbsoluteExpiry --
//   it automatically satisfies the ItemCache interface. No declaration needed.
package service

import (
	"context"
	"time"

	"cachecraft/internal/domain"
)

// ItemRepository is the port for reading items from persistent storage.
//
// context.Context is how Go passes request-scoped values and deadlines across
// function calls. ALWAYS pass ctx as the first argument in Go. It allows the
// caller to cancel work in progress (e.g. when the HTTP client disconnects,
// or when the 2-second pool timeout fires). Think of it as the Go equivalent
// of passing a request object through the entire call stack.
type ItemRepository interface {
	// FindByID fetches a single item by its primary key.
	// delayMillis is injected into the SQL query as a pg_sleep argument to
	// artificially hold a database connection open, simulating slow queries.
	FindByID(ctx context.Context, id int64, delayMillis int64) (domain.Item, error)

	// FindAll fetches every item in the table. Used by bulk-warm to load
	// all 10,000 seeded items into Redis with one SQL statement.
	FindAll(ctx context.Context) ([]domain.Item, error)

	// QueryCount returns the number of SQL SELECT statements this process
	// has submitted to PostgreSQL. Used by /debug/db-queries to measure the
	// "query delta" before and after a load test run.
	QueryCount() int64
}

// ItemCache is the port for reading and writing items to the cache layer.
type ItemCache interface {
	// Get looks up an item in the cache by its ID.
	// Returns (item, true, nil) on a cache hit.
	// Returns (zero-value, false, nil) on a normal cache miss.
	// Returns (zero-value, false, err) if something went wrong (e.g. deserialization failure).
	Get(ctx context.Context, id int64) (domain.Item, bool, error)

	// Put stores an item in the cache with a TTL.
	// jitterFraction applies symmetric random TTL variation: 0.0 = no jitter,
	// 0.10 = TTL randomly adjusted by +/- 10%.
	Put(ctx context.Context, item domain.Item, ttl time.Duration, jitterFraction float64) error

	// PutAllWithAbsoluteExpiry stores many items with a shared absolute
	// expiration time (same millisecond for all). Used in the avalanche
	// experiment to synchronize key expirations intentionally.
	PutAllWithAbsoluteExpiry(ctx context.Context, items []domain.Item, expiresAt time.Time) error
}

// CacheResetter is a separate port for the destructive test-reset operation.
// It is kept separate from ItemCache because it is only exposed in "local" mode.
// The service that handles bulk-warm and reset is its own type so the router
// can conditionally register the reset route.
type CacheResetter interface {
	Reset(ctx context.Context) (domain.CacheResetResult, error)
}

// LocalCoalescer coalesces concurrent requests for the same key within a
// single process. Only one goroutine executes the provided function; all
// other goroutines that arrive while it is running block and receive the
// same result when it completes.
//
// This is the Go equivalent of Java's LocalLockService using ConcurrentHashMap
// and ReentrantLock, but the entire complexity is hidden in singleflight.Group.
type LocalCoalescer interface {
	// Do runs fn for key exactly once at a time. Concurrent calls with the
	// same key block until fn returns, then all callers receive fn's result.
	Do(key string, fn func() (domain.Item, error)) (domain.Item, error)
}

// DistributedLocker coalesces requests across the entire cluster by electing
// one leader in Redis. All other nodes wait for the leader to populate the
// cache, then serve the result without hitting the database.
type DistributedLocker interface {
	// GetOrLoad attempts to serve an item from cache. If the item is missing,
	// it elects a leader across the cluster using Redis SET NX.
	// The leader loads from the DB and populates the cache.
	// Followers poll the cache with jittered intervals until the value appears.
	GetOrLoad(
		ctx context.Context,
		itemID int64,
		delayMillis int64,
		cacheLookup func() (domain.Item, bool, error),
		loadAndCache func() (domain.Item, error),
	) (domain.CacheResponse, error)
}
