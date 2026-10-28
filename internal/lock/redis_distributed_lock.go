// Package lock contains the two request coalescing strategies for CacheCraft.
//
// This file implements the "distributed-lock" strategy: cluster-wide
// request coalescing using Redis as a distributed coordination primitive.
//
// DEPENDENCY: github.com/redis/go-redis/v9
//   Same client used by the cache adapter. We reuse it here for lock operations.
//
// HOW THE DISTRIBUTED LOCK WORKS:
//
//   Leader election (SET NX):
//     Redis command: SET lock:item:42 <uuid-token> NX PX 5000
//     NX means "set only if Not eXists" -- this is the atomic election.
//     PX 5000 means the lock auto-expires after 5000ms if the leader crashes.
//     If Redis returns OK: this goroutine is the LEADER.
//     If Redis returns nil: this goroutine is a FOLLOWER.
//
//   Leader behavior:
//     1. Double-check the cache (another leader may have just populated it).
//     2. If still empty: load from the database and write to Redis.
//     3. Release the lock via a Lua script (atomic check-and-delete).
//
//   Follower behavior:
//     1. Enter a polling loop, sleeping for jittered intervals (5ms to 50ms).
//     2. After each sleep, check if the cache now has the item.
//     3. If the total wait exceeds the deadline: attempt one final election.
//     4. If the final election also fails: return ErrLockUnavailable.
//
//   Safe lock release (Lua script):
//     We use a Lua script to atomically verify that we still own the lock
//     before deleting it. Without this check, the following race is possible:
//       1. Leader finishes but the lock's TTL expires due to a slow operation.
//       2. A new leader acquires the same lock key.
//       3. The original leader wakes up and deletes the new leader's lock.
//     The Lua script prevents step 3 by comparing the token stored in Redis
//     against the token held by this goroutine. If they differ, we do nothing.
//     Lua scripts in Redis execute atomically -- no other Redis command runs
//     between the GET and the DEL inside the script.
package lock

import (
	"context"
	"fmt"
	"time"

	"cachecraft/internal/domain"
	"cachecraft/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// releaseLockScript is the Lua script for safe lock release.
// KEYS[1] is the lock key (e.g. "lock:item:42").
// ARGV[1] is the UUID token this process holds.
//
// The script runs atomically in Redis -- no interleaving possible.
const releaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
else
    return 0
end`

// RedisDistributedLock implements service.DistributedLocker.
// It coordinates cache-miss resolution across all nodes in the cluster.
type RedisDistributedLock struct {
	rdb       *redis.Client
	lockLease time.Duration // How long a Redis lock lives before auto-expiring.
}

// NewRedisDistributedLock creates a new RedisDistributedLock.
func NewRedisDistributedLock(rdb *redis.Client, lockLease time.Duration) *RedisDistributedLock {
	return &RedisDistributedLock{
		rdb:       rdb,
		lockLease: lockLease,
	}
}

// GetOrLoad is the main entry point for the distributed-lock strategy.
// See service.DistributedLocker for the contract documentation.
func (l *RedisDistributedLock) GetOrLoad(
	ctx context.Context,
	itemID int64,
	delayMillis int64,
	cacheLookup func() (domain.Item, bool, error),
	loadAndCache func() (domain.Item, error),
) (domain.CacheResponse, error) {
	// Step 1: Fast path -- check cache before attempting any lock.
	if item, hit, err := cacheLookup(); err != nil {
		return domain.CacheResponse{}, err
	} else if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	lockKey := lockKey(itemID)

	// Step 2: Attempt to win the leader election.
	token, acquired := l.tryAcquire(ctx, lockKey)
	if acquired {
		return l.runAsLeader(ctx, lockKey, token, cacheLookup, loadAndCache)
	}

	// Step 3: We are a follower. Wait for the leader to populate the cache.
	maxWait := service.FollowerMaxWait(delayMillis)
	baseInterval := service.PollBase(delayMillis)

	item, found, err := l.awaitCacheValue(ctx, cacheLookup, maxWait, baseInterval)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	if found {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	// Step 4: The follower timed out. Try one final election before giving up.
	// This handles the edge case where the leader's lock expired just as this
	// follower reached its deadline.
	token, acquired = l.tryAcquire(ctx, lockKey)
	if !acquired {
		return domain.CacheResponse{}, fmt.Errorf("%w: item %d", domain.ErrLockUnavailable, itemID)
	}

	return l.runAsLeader(ctx, lockKey, token, cacheLookup, loadAndCache)
}

// runAsLeader executes the cache-fill operation as the elected leader.
// It always releases the lock on exit, even if loading from the DB fails.
func (l *RedisDistributedLock) runAsLeader(
	ctx context.Context,
	lockKey string,
	token string,
	cacheLookup func() (domain.Item, bool, error),
	loadAndCache func() (domain.Item, error),
) (domain.CacheResponse, error) {
	// "defer" ensures the lock is always released when this function exits.
	// This is like Java's try-finally block.
	defer l.releaseIfOwned(ctx, lockKey, token)

	// Double-check the cache: a previous leader may have populated it
	// between our last check and us winning the election.
	if item, hit, err := cacheLookup(); err != nil {
		return domain.CacheResponse{}, err
	} else if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	item, err := loadAndCache()
	if err != nil {
		return domain.CacheResponse{}, err
	}

	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

// awaitCacheValue polls the cache with jittered intervals until the item
// appears or the total wait duration is exceeded.
func (l *RedisDistributedLock) awaitCacheValue(
	ctx context.Context,
	cacheLookup func() (domain.Item, bool, error),
	maxWait time.Duration,
	baseInterval time.Duration,
) (domain.Item, bool, error) {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		item, found, err := cacheLookup()
		if err != nil {
			return domain.Item{}, false, err
		}
		if found {
			return item, true, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		// Sleep for a jittered interval, but never longer than the remaining wait.
		interval := service.JitteredPollInterval(baseInterval)
		if interval > remaining {
			interval = remaining
		}

		// time.Sleep blocks the current goroutine (not an OS thread) for the
		// specified duration. This is cheap in Go because goroutines are parked
		// by the runtime scheduler, not blocking an OS thread.
		time.Sleep(interval)
	}

	return domain.Item{}, false, nil
}

// tryAcquire attempts to win the distributed lock election.
// Returns (token, true) if the lock was acquired, ("", false) otherwise.
//
// uuid.NewString() generates a random UUID token (e.g. "550e8400-e29b-41d4-...").
// We store this token as the lock's value so we can verify ownership before release.
func (l *RedisDistributedLock) tryAcquire(ctx context.Context, lockKey string) (string, bool) {
	token := uuid.NewString()
	result, err := l.rdb.SetNX(ctx, lockKey, token, l.lockLease).Result()
	if err != nil || !result {
		return "", false
	}
	return token, true
}

// releaseIfOwned runs the Lua script to atomically delete the lock only if
// the stored token still matches the token we hold.
func (l *RedisDistributedLock) releaseIfOwned(ctx context.Context, lockKey string, token string) {
	// redis.NewScript creates a script object. go-redis caches the script's SHA1
	// hash and sends EVALSHA instead of the full script body on repeat calls.
	script := redis.NewScript(releaseLockScript)
	script.Run(ctx, l.rdb, []string{lockKey}, token)
}

// lockKey returns the Redis key for a distributed lock.
// Example: lockKey(42) returns "lock:item:42"
func lockKey(itemID int64) string {
	return fmt.Sprintf("%s%d", "lock:item:", itemID)
}
