package service

import (
	"context"
	"time"

	"cachecraft/internal/domain"
)

// ItemRepository defines data access methods for persistent item storage.
type ItemRepository interface {
	FindByID(ctx context.Context, id int64, delayMillis int64) (domain.Item, error)
	FindAll(ctx context.Context) ([]domain.Item, error)
	QueryCount() int64
}

// ItemCache defines methods for reading, writing, and bulk-warming the cache.
type ItemCache interface {
	Get(ctx context.Context, id int64) (domain.Item, bool, error)
	Put(ctx context.Context, item domain.Item, ttl time.Duration, jitterFraction float64) error
	PutAllWithAbsoluteExpiry(ctx context.Context, items []domain.Item, expiresAt time.Time) error
}

// CacheResetter defines methods for clearing test cache keys.
type CacheResetter interface {
	Reset(ctx context.Context) (domain.CacheResetResult, error)
}

// LocalCoalescer deduplicates concurrent in-flight requests within a single process.
type LocalCoalescer interface {
	Do(key string, fn func() (domain.Item, error)) (domain.Item, error)
}

// DistributedLocker coordinates cache population across a multi-node cluster.
type DistributedLocker interface {
	GetOrLoad(
		ctx context.Context,
		itemID int64,
		delayMillis int64,
		cacheLookup func() (domain.Item, bool, error),
		loadAndCache func() (domain.Item, error),
	) (domain.CacheResponse, error)
}
