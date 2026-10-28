// Package lock contains the two request coalescing strategies for CacheCraft:
//
//  1. SingleFlightLock (this file): In-process coalescing using Go's
//     golang.org/x/sync/singleflight package.
//  2. RedisDistributedLock (redis_distributed_lock.go): Cluster-wide
//     coalescing using Redis SET NX.
//
// This file implements the "local-single-flight" strategy.
//
// DEPENDENCY: golang.org/x/sync/singleflight
//   "golang.org/x/sync" is the "extended standard library" -- packages that
//   the Go team maintains but ships separately from the core stdlib, because
//   they are more specialized. The "x/" prefix stands for "extended".
//
//   singleflight.Group solves the cache stampede problem at the single-process
//   level. When multiple goroutines call Do() with the same key concurrently,
//   only the FIRST call actually executes the function. All subsequent callers
//   block on a channel and receive the same return value when the first call
//   completes. This is also called "request coalescing" or "duplicate suppression".
//
// HOW singleflight.Group works internally (simplified):
//   - It has an internal map: map[string]*call
//   - On Do("item:42", fn):
//     * If "item:42" is NOT in the map: create a new call, add it to the map,
//       run fn() in the current goroutine, remove from map, broadcast result.
//     * If "item:42" IS in the map: the goroutine blocks on a channel waiting
//       for the existing call to finish.
//   - The third return value (bool) indicates whether the result was shared
//     (true = this goroutine was a follower, false = it was the leader).
//
// DIFFERENCE from the Java version:
//   Java's LocalLockService managed a ConcurrentHashMap<String, LockEntry>
//   with reference counting to clean up unused locks. That was ~50 lines.
//   In Go, singleflight.Group handles all of this internally in ~3 lines.
package lock

import (
	"fmt"

	"cachecraft/internal/domain"
	"golang.org/x/sync/singleflight"
)

// SingleFlightLock implements service.LocalCoalescer using singleflight.Group.
// It ensures that for any given item key, only one goroutine in this process
// executes the cache-miss handler at a time.
type SingleFlightLock struct {
	group singleflight.Group
}

// NewSingleFlightLock creates and returns a new SingleFlightLock.
// singleflight.Group does not need initialization; its zero value is ready to use.
func NewSingleFlightLock() *SingleFlightLock {
	return &SingleFlightLock{}
}

// Do runs fn for the given key with single-flight deduplication.
//
// If fn is already running for this key (because another goroutine called Do
// with the same key concurrently), the caller blocks until that running fn
// completes, then returns the same result.
//
// The key typically looks like "item:42" or "sf:item:42" to prevent accidental
// collisions with Redis key namespaces. We use "sf:" as a prefix to make it
// explicit that this key is only for in-process singleflight coordination.
//
// TYPE ASSERTION: group.Do returns (any, error, bool).
//   "any" is Go's alias for the empty interface "interface{}".
//   Since our fn returns domain.Item, we need to assert the type back:
//   result.(domain.Item) tells Go "I know this 'any' is actually a domain.Item".
//   If the assertion is wrong, it panics -- but here it is guaranteed to be
//   correct because we always return domain.Item from the function we pass in.
func (s *SingleFlightLock) Do(key string, fn func() (domain.Item, error)) (domain.Item, error) {
	// We prefix with "sf:" to clearly separate singleflight keys from Redis keys.
	sfKey := fmt.Sprintf("sf:%s", key)

	// group.Do signature: Do(key string, fn func() (any, error)) (any, error, bool)
	result, err, _ := s.group.Do(sfKey, func() (any, error) {
		return fn()
	})

	if err != nil {
		return domain.Item{}, err
	}

	// Type assertion: convert the "any" returned by group.Do back to domain.Item.
	// The "ok" pattern: item, ok := result.(domain.Item)
	// If ok is false, the value is NOT a domain.Item and something went wrong.
	item, ok := result.(domain.Item)
	if !ok {
		return domain.Item{}, fmt.Errorf("singleflight returned unexpected type for key %s", key)
	}

	return item, nil
}
