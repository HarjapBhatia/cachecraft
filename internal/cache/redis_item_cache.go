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

const (
	itemKeyPrefix  = "item:"
	lockKeyPrefix  = "lock:item:"
	itemKeyPattern = "item:*"
	lockKeyPattern = "lock:item:*"
	minimumTTL     = time.Millisecond
)

// RedisItemCache implements service.ItemCache and service.CacheResetter using go-redis.
type RedisItemCache struct {
	rdb *redis.Client
}

// NewRedisItemCache constructs a new RedisItemCache adapter.
func NewRedisItemCache(rdb *redis.Client) *RedisItemCache {
	return &RedisItemCache{rdb: rdb}
}

// Get fetches and deserializes an item from Redis.
func (c *RedisItemCache) Get(ctx context.Context, id int64) (domain.Item, bool, error) {
	data, err := c.rdb.Get(ctx, itemKey(id)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return domain.Item{}, false, nil
		}
		return domain.Item{}, false, fmt.Errorf("redis GET failed for key %s: %w", itemKey(id), err)
	}

	var item domain.Item
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		return domain.Item{}, false, fmt.Errorf("failed to deserialize item %d: %w", id, err)
	}

	return item, true, nil
}

// Put serializes and stores an item with an optionally jittered TTL.
func (c *RedisItemCache) Put(ctx context.Context, item domain.Item, ttl time.Duration, jitterFraction float64) error {
	effectiveTTL := effectiveTTLWithJitter(ttl, jitterFraction)

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to serialize item %d: %w", item.ID, err)
	}

	if err := c.rdb.Set(ctx, itemKey(item.ID), data, effectiveTTL).Err(); err != nil {
		return fmt.Errorf("redis SET failed for item %d: %w", item.ID, err)
	}

	return nil
}

// PutAllWithAbsoluteExpiry writes multiple items in a pipeline with identical expiration times.
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
			ttl := time.Until(expiresAt)
			pipe.Set(ctx, itemKey(item.ID), data, ttl)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("redis pipeline bulk-warm failed: %w", err)
	}

	return nil
}

// Reset clears all item and lock keys from Redis.
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

func (c *RedisItemCache) deleteMatching(ctx context.Context, pattern string) (int64, error) {
	keys, err := c.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return 0, fmt.Errorf("redis KEYS %s failed: %w", pattern, err)
	}

	if len(keys) == 0 {
		return 0, nil
	}

	deleted, err := c.rdb.Del(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("redis DEL failed for pattern %s: %w", pattern, err)
	}

	return deleted, nil
}

func itemKey(id int64) string {
	return fmt.Sprintf("%s%d", itemKeyPrefix, id)
}

func effectiveTTLWithJitter(base time.Duration, jitterFraction float64) time.Duration {
	if jitterFraction == 0.0 {
		if base < minimumTTL {
			return minimumTTL
		}
		return base
	}

	adjustment := (rand.Float64() - 0.5) * 2.0 * jitterFraction
	effective := time.Duration(float64(base) * (1.0 + adjustment))
	if effective < minimumTTL {
		return minimumTTL
	}
	return effective
}
