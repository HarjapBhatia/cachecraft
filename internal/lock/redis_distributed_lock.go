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

const releaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
else
    return 0
end`

// RedisDistributedLock coordinates cluster-wide cache population using Redis SET NX and Lua release.
type RedisDistributedLock struct {
	rdb       *redis.Client
	lockLease time.Duration
}

// NewRedisDistributedLock creates a new RedisDistributedLock instance.
func NewRedisDistributedLock(rdb *redis.Client, lockLease time.Duration) *RedisDistributedLock {
	return &RedisDistributedLock{
		rdb:       rdb,
		lockLease: lockLease,
	}
}

// GetOrLoad coordinates leader election and follower polling across the cluster.
func (l *RedisDistributedLock) GetOrLoad(
	ctx context.Context,
	itemID int64,
	delayMillis int64,
	cacheLookup func() (domain.Item, bool, error),
	loadAndCache func() (domain.Item, error),
) (domain.CacheResponse, error) {
	if item, hit, err := cacheLookup(); err != nil {
		return domain.CacheResponse{}, err
	} else if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	lockKey := lockKey(itemID)

	token, acquired := l.tryAcquire(ctx, lockKey)
	if acquired {
		return l.runAsLeader(ctx, lockKey, token, cacheLookup, loadAndCache)
	}

	maxWait := service.FollowerMaxWait(delayMillis)
	baseInterval := service.PollBase(delayMillis)

	item, found, err := l.awaitCacheValue(ctx, cacheLookup, maxWait, baseInterval)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	if found {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	token, acquired = l.tryAcquire(ctx, lockKey)
	if !acquired {
		return domain.CacheResponse{}, fmt.Errorf("%w: item %d", domain.ErrLockUnavailable, itemID)
	}

	return l.runAsLeader(ctx, lockKey, token, cacheLookup, loadAndCache)
}

func (l *RedisDistributedLock) runAsLeader(
	ctx context.Context,
	lockKey string,
	token string,
	cacheLookup func() (domain.Item, bool, error),
	loadAndCache func() (domain.Item, error),
) (domain.CacheResponse, error) {
	defer l.releaseIfOwned(ctx, lockKey, token)

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

		interval := service.JitteredPollInterval(baseInterval)
		if interval > remaining {
			interval = remaining
		}

		time.Sleep(interval)
	}

	return domain.Item{}, false, nil
}

func (l *RedisDistributedLock) tryAcquire(ctx context.Context, lockKey string) (string, bool) {
	token := uuid.NewString()
	result, err := l.rdb.SetNX(ctx, lockKey, token, l.lockLease).Result()
	if err != nil || !result {
		return "", false
	}
	return token, true
}

func (l *RedisDistributedLock) releaseIfOwned(ctx context.Context, lockKey string, token string) {
	script := redis.NewScript(releaseLockScript)
	script.Run(ctx, l.rdb, []string{lockKey}, token)
}

func lockKey(itemID int64) string {
	return fmt.Sprintf("lock:item:%d", itemID)
}
