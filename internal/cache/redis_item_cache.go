// Package cache contains the Redis adapter for the CacheCraft application.
// It implements the service.ItemCache and service.CacheResetter interfaces.
//
// DEPENDENCY: github.com/redis/go-redis/v9
//   go-redis is the most widely used Redis client for Go. The v9 suffix means
//   major version 9, which is the version with full context.Context support.
//   Every Redis operation accepts a context, so it can be cancelled or timed out.
//
// HOW go-redis works:
//   1. redis.NewClient(&redis.Options{Addr: "localhost:6379"}) creates a client.
//      The client manages a connection pool internally.
//   2. rdb.Get(ctx, key) sends a GET command and returns a *redis.StringCmd.
//   3. .Result() on a *redis.StringCmd gives you (string, error).
//   4. If the key does not exist, the error is redis.Nil (a sentinel value,
//      like sql.ErrNoRows in database/sql).
//
// KEY NAMING CONVENTION:
//   All item keys are stored as "item:{id}".
//   All lock keys are stored as "lock:item:{id}".
//   This namespacing lets us run KEYS "item:*" to find only item keys,
//   and KEYS "lock:item:*" to find only lock keys, without overlap.
//
// SERIALIZATION:
//   Items are serialized to JSON strings before being stored in Redis.
//   Go's standard library "encoding/json" handles this. It is equivalent
//   to Jackson's ObjectMapper in the Java version.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"cachecraft/internal/domain"
	"github.com/redis/go-redis/v9"
)

// Key prefixes. Using constants prevents typos from causing subtle key mismatches.
const (
	itemKeyPrefix = "item:"
	lockKeyPrefix = "lock:item:"

	// itemKeyPattern is the Redis KEYS glob used to find all item entries.
	// This is used in clearItems and Reset operations.
	itemKeyPattern = "item:*"

	// lockKeyPattern is the Redis KEYS glob used to find all lock entries.
	lockKeyPattern = "lock:item:*"

	// minimumTTL ensures we never store a key with a zero or negative TTL,
	// which would cause Redis to not store it at all.
	minimumTTL = time.Millisecond
)

// RedisItemCache implements service.ItemCache using the go-redis client.
// It also implements service.CacheResetter for the test reset operation.
type RedisItemCache struct {
	rdb *redis.Client
}

// NewRedisItemCache creates and returns a new RedisItemCache.
// The caller (main.go) creates the redis.Client and injects it here.
func NewRedisItemCache(rdb *redis.Client) *RedisItemCache {
	return &RedisItemCache{rdb: rdb}
}

// Get looks up an item in Redis by ID.
//
// Return values:
//   (item, true, nil)   -- cache hit: item found and deserialized successfully
//   (zero, false, nil)  -- cache miss: key does not exist in Redis
//   (zero, false, err)  -- error: unexpected Redis problem or bad JSON
//
// "zero" here means domain.Item{} -- a struct with all fields at their zero
// values (0 for numbers, "" for strings). Go does not have null for structs;
// instead, you check the bool return value (the second return).
func (c *RedisItemCache) Get(ctx context.Context, id int64) (domain.Item, bool, error) {
	data, err := c.rdb.Get(ctx, itemKey(id)).Result()
	if err != nil {
		// redis.Nil is the sentinel error returned when a key does not exist.
		// This is a normal cache miss, not an error condition.
		if errors.Is(err, redis.Nil) {
			return domain.Item{}, false, nil
		}
		return domain.Item{}, false, fmt.Errorf("Redis GET failed for key %s: %w", itemKey(id), err)
	}

	var item domain.Item
	// json.Unmarshal parses a JSON byte slice into a Go struct.
	// []byte(data) converts the string from Redis into a byte slice.
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		return domain.Item{}, false, fmt.Errorf("failed to deserialize item %d from cache: %w", id, err)
	}

	return item, true, nil
}

// Put writes an item to Redis with the given TTL, optionally jittered.
//
// JITTER MECHANICS:
//   TTL jitter desynchronizes cache expirations to prevent cache avalanches.
//   If jitterFraction = 0.10, the effective TTL is randomly chosen from:
//     [ ttl * 0.90, ttl * 1.10 ]   (a uniform distribution)
//
//   This means keys expire at slightly different times, smoothing out the
//   query spike that would otherwise hit the database simultaneously.
//
//   Formula: effectiveTTL = ttl * (1.0 + rand.Float64InRange(-jitter, +jitter))
//
//   rand.Float64() returns a value in [0.0, 1.0).
//   We scale and shift it to get a value in [-jitterFraction, +jitterFraction).
func (c *RedisItemCache) Put(ctx context.Context, item domain.Item, ttl time.Duration, jitterFraction float64) error {
	effectiveTTL := effectiveTTLWithJitter(ttl, jitterFraction)

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to serialize item %d for cache: %w", item.ID, err)
	}

	// rdb.Set(ctx, key, value, ttl) executes Redis SET key value PX ttlMilliseconds
	if err := c.rdb.Set(ctx, itemKey(item.ID), data, effectiveTTL).Err(); err != nil {
		return fmt.Errorf("Redis SET failed for item %d: %w", item.ID, err)
	}

	return nil
}

// PutAllWithAbsoluteExpiry writes many items to Redis in a single pipeline,
// all expiring at the exact same absolute timestamp.
//
// WHY a pipeline?
//   Without pipelining, writing 10,000 items requires 10,000 round trips to
//   Redis (one GET/SET per network call). With a pipeline, all 10,000 SET
//   commands are buffered and sent in a single network batch, then Redis
//   executes them all and returns all responses at once.
//
// WHY absolute expiry?
//   The avalanche experiment requires all 10,000 keys to expire at the same
//   millisecond. If we used a relative TTL (e.g. "expire in 5 seconds"),
//   the loop itself takes time, causing the last item written to expire ~1s
//   later than the first. Absolute expiry (ExpireAt) eliminates this drift.
//
// HOW go-redis pipeline works:
//   rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error { ... })
//   Inside the callback, you issue commands on "pipe" instead of "rdb".
//   They are queued locally and flushed in one batch when the callback returns.
func (c *RedisItemCache) PutAllWithAbsoluteExpiry(ctx context.Context, items []domain.Item, expiresAt time.Time) error {
	if len(items) == 0 {
		return nil
	}

	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("absolute cache expiry must be in the future")
	}

	_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, item := range items {
			data, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("failed to serialize item %d: %w", item.ID, err)
			}
			// SetEx sets a key with a relative TTL. We compute "how long from now
			// until expiresAt?" to convert the absolute timestamp to a relative duration.
			ttl := time.Until(expiresAt)
			pipe.Set(ctx, itemKey(item.ID), data, ttl)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("Redis pipeline for bulk warm failed: %w", err)
	}

	return nil
}

// Reset deletes all item and lock keys from Redis. This is the destructive
// operation used between experiments to ensure a clean cache state.
// Only registered in "local" mode by the router.
//
// redis KEYS command returns all keys matching a glob pattern.
// WARNING: KEYS is O(N) over all keys in Redis. Safe for local experiments
// with bounded key counts, but do NOT use KEYS in a production environment
// with millions of keys. Use SCAN instead in production.
func (c *RedisItemCache) Reset(ctx context.Context) (domain.CacheResetResult, error) {
	deletedItems, err := c.deleteMatching(ctx, itemKeyPattern)
	if err != nil {
		return domain.CacheResetResult{}, fmt.Errorf("failed to delete item keys: %w", err)
	}

	deletedLocks, err := c.deleteMatching(ctx, lockKeyPattern)
	if err != nil {
		return domain.CacheResetResult{}, fmt.Errorf("failed to delete lock keys: %w", err)
	}

	return domain.CacheResetResult{
		DeletedItemKeys: deletedItems,
		DeletedLockKeys: deletedLocks,
	}, nil
}

// deleteMatching finds all Redis keys matching a glob pattern and deletes them.
// Returns the number of keys deleted.
func (c *RedisItemCache) deleteMatching(ctx context.Context, pattern string) (int64, error) {
	keys, err := c.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return 0, fmt.Errorf("Redis KEYS %s failed: %w", pattern, err)
	}

	if len(keys) == 0 {
		return 0, nil
	}

	// Del accepts variadic arguments. The "..." unpacks the slice into individual args.
	// In Python this would be rdb.delete(*keys). In Go it's rdb.Del(ctx, keys...).
	deleted, err := c.rdb.Del(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("Redis DEL failed for pattern %s: %w", pattern, err)
	}

	return deleted, nil
}

// itemKey returns the Redis key for an item.
// Example: itemKey(42) returns "item:42"
func itemKey(id int64) string {
	return fmt.Sprintf("%s%d", itemKeyPrefix, id)
}

// effectiveTTLWithJitter applies symmetric uniform random jitter to a base TTL.
// If jitterFraction is 0.0, the base TTL is returned unchanged.
//
// The "math" (explained in plain terms):
//   base  = 5000ms
//   jitter = 0.10
//
//   rand.Float64() gives a value in [0.0, 1.0), e.g. 0.73
//   Shift to [-0.5, 0.5):   0.73 - 0.5 = 0.23
//   Scale to [-0.10, 0.10): 0.23 * 2 * 0.10 = 0.046
//   effectiveTTL = 5000ms * (1.0 + 0.046) = 5230ms
//
// This means the key will expire 230ms LATER than baseline.
// Another key might get -0.037, expiring 185ms EARLIER than baseline.
// Over 10,000 keys, expirations are uniformly spread across a 1000ms window
// (5% below to 5% above the 5-second baseline... wait, actually it's 10%).
func effectiveTTLWithJitter(base time.Duration, jitterFraction float64) time.Duration {
	if jitterFraction == 0.0 {
		if base < minimumTTL {
			return minimumTTL
		}
		return base
	}

	// rand.Float64() is in [0.0, 1.0)
	// We scale it to be in [-jitterFraction, +jitterFraction)
	adjustment := (rand.Float64() - 0.5) * 2.0 * jitterFraction
	effective := time.Duration(float64(base) * (1.0 + adjustment))
	if effective < minimumTTL {
		return minimumTTL
	}
	return effective
}
