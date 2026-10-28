package service

import (
	"context"
	"fmt"
	"time"

	"cachecraft/internal/domain"
)

// ItemService coordinates caching strategies for item retrieval.
type ItemService struct {
	repo    ItemRepository
	cache   ItemCache
	local   LocalCoalescer
	distrib DistributedLocker
}

// NewItemService creates a new ItemService instance with the provided adapters.
func NewItemService(repo ItemRepository, cache ItemCache, local LocalCoalescer, distrib DistributedLocker) *ItemService {
	return &ItemService{
		repo:    repo,
		cache:   cache,
		local:   local,
		distrib: distrib,
	}
}

// GetItem retrieves an item using the specified caching strategy.
func (s *ItemService) GetItem(
	ctx context.Context,
	id int64,
	strategy string,
	ttl time.Duration,
	delayMillis int64,
	jitterFraction float64,
) (domain.CacheResponse, error) {
	switch strategy {
	case "no-cache":
		return s.loadNoCache(ctx, id, delayMillis)

	case "cache-aside":
		return s.loadCacheAside(ctx, id, ttl, delayMillis, 0.0)

	case "cache-aside-jitter":
		return s.loadCacheAside(ctx, id, ttl, delayMillis, jitterFraction)

	case "local-single-flight":
		return s.loadWithLocalSingleFlight(ctx, id, ttl, delayMillis)

	case "distributed-lock":
		return s.loadWithDistributedLock(ctx, id, ttl, delayMillis)

	default:
		return domain.CacheResponse{}, fmt.Errorf("%w: %s", domain.ErrUnsupportedStrategy, strategy)
	}
}

func (s *ItemService) loadNoCache(ctx context.Context, id int64, delayMillis int64) (domain.CacheResponse, error) {
	item, err := s.repo.FindByID(ctx, id, delayMillis)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

func (s *ItemService) loadCacheAside(
	ctx context.Context,
	id int64,
	ttl time.Duration,
	delayMillis int64,
	jitterFraction float64,
) (domain.CacheResponse, error) {
	item, hit, err := s.cache.Get(ctx, id)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	item, err = s.repo.FindByID(ctx, id, delayMillis)
	if err != nil {
		return domain.CacheResponse{}, err
	}

	_ = s.cache.Put(ctx, item, ttl, jitterFraction)

	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

func (s *ItemService) loadWithLocalSingleFlight(
	ctx context.Context,
	id int64,
	ttl time.Duration,
	delayMillis int64,
) (domain.CacheResponse, error) {
	item, hit, err := s.cache.Get(ctx, id)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	sfKey := fmt.Sprintf("item:%d", id)

	item, err = s.local.Do(sfKey, func() (domain.Item, error) {
		if itemAfterLock, hitAfterLock, errCheck := s.cache.Get(ctx, id); errCheck == nil && hitAfterLock {
			return itemAfterLock, nil
		}

		loaded, loadErr := s.repo.FindByID(ctx, id, delayMillis)
		if loadErr != nil {
			return domain.Item{}, loadErr
		}

		_ = s.cache.Put(ctx, loaded, ttl, 0.0)
		return loaded, nil
	})

	if err != nil {
		return domain.CacheResponse{}, err
	}

	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

func (s *ItemService) loadWithDistributedLock(
	ctx context.Context,
	id int64,
	ttl time.Duration,
	delayMillis int64,
) (domain.CacheResponse, error) {
	if delayMillis >= 1000 {
		return domain.CacheResponse{}, fmt.Errorf("%w: got %dms", domain.ErrInvalidLockDelay, delayMillis)
	}

	return s.distrib.GetOrLoad(
		ctx,
		id,
		delayMillis,
		func() (domain.Item, bool, error) {
			return s.cache.Get(ctx, id)
		},
		func() (domain.Item, error) {
			item, err := s.repo.FindByID(ctx, id, delayMillis)
			if err != nil {
				return domain.Item{}, err
			}
			_ = s.cache.Put(ctx, item, ttl, 0.0)
			return item, nil
		},
	)
}
