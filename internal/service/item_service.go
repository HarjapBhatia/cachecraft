// Package service contains the core application logic for the CacheCraft application.
//
// This file contains ItemService -- the main coordinator that selects and
// executes the appropriate caching strategy based on the "strategy" query
// parameter from the HTTP request.
//
// ItemService is equivalent to ItemService.java in the Spring Boot version.
// It knows about ALL the strategies but delegates actual work to the ports
// (interfaces) it receives at construction time.
//
// STRATEGIES IMPLEMENTED:
//   "no-cache"           -- Always query PostgreSQL, skip the cache entirely.
//   "cache-aside"        -- Standard read-through: check cache, miss = query DB + store.
//   "cache-aside-jitter" -- Same as cache-aside but applies TTL jitter on write.
//   "local-single-flight"-- Cache-aside with in-process request coalescing.
//   "distributed-lock"   -- Cache-aside with cluster-wide request coalescing.
//
// DEPENDENCY INJECTION:
//   ItemService receives its dependencies through its constructor (NewItemService).
//   It only knows about the interface types (ItemRepository, ItemCache, etc.)
//   defined in ports.go. It has zero knowledge of PostgreSQL, Redis, or go-redis.
//   This is the Dependency Inversion Principle applied: high-level logic does
//   not depend on low-level infrastructure.
package service

import (
	"context"
	"fmt"
	"time"

	"cachecraft/internal/domain"
)

// ItemService coordinates caching strategies for item reads.
// All fields are unexported (lowercase) -- they are internal implementation details.
type ItemService struct {
	repo   ItemRepository
	cache  ItemCache
	local  LocalCoalescer
	distrib DistributedLocker
}

// NewItemService creates a new ItemService with all required dependencies.
// In Go, there is no @Autowired or DI container. You wire dependencies
// explicitly in main.go by passing concrete structs that satisfy the interfaces.
func NewItemService(repo ItemRepository, cache ItemCache, local LocalCoalescer, distrib DistributedLocker) *ItemService {
	return &ItemService{
		repo:    repo,
		cache:   cache,
		local:   local,
		distrib: distrib,
	}
}

// GetItem routes the request to the appropriate caching strategy.
// It is called by the HTTP handler for every GET /api/items/{id} request.
//
// Parameters:
//   ctx           -- request context with the 2-second deadline for pool timeout.
//   id            -- the item ID from the URL path.
//   strategy      -- which caching approach to use (from query param ?strategy=...).
//   ttl           -- how long to cache the item (from query param ?ttl=...).
//   delayMillis   -- how many milliseconds pg_sleep holds the DB connection.
//   jitterFraction-- how much random TTL variation to apply (0.0 to 1.0).
//
// Returns a CacheResponse (item + cacheHit flag) or an error.
func (s *ItemService) GetItem(
	ctx context.Context,
	id int64,
	strategy string,
	ttl time.Duration,
	delayMillis int64,
	jitterFraction float64,
) (domain.CacheResponse, error) {
	// Go's "switch" on a string is clean and idiomatic.
	// No fallthrough by default (unlike C/Java where you need explicit "break").
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
		// The "fmt.Errorf" function creates a new error with a formatted message.
		// "%w" is the "wrap" verb -- it wraps domain.ErrUnsupportedStrategy so the
		// caller can check "errors.Is(err, domain.ErrUnsupportedStrategy)".
		return domain.CacheResponse{}, fmt.Errorf("%w: %s", domain.ErrUnsupportedStrategy, strategy)
	}
}

// loadNoCache always queries PostgreSQL, bypassing the cache layer entirely.
// Used for the baseline saturation experiment.
func (s *ItemService) loadNoCache(ctx context.Context, id int64, delayMillis int64) (domain.CacheResponse, error) {
	item, err := s.repo.FindByID(ctx, id, delayMillis)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

// loadCacheAside implements the standard cache-aside (lazy loading) pattern:
//   1. Check the cache.
//   2. On a hit: return the cached item.
//   3. On a miss: load from DB, write to cache, return the item.
//
// When jitterFraction > 0, TTL is randomly adjusted to prevent cache avalanches.
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

	// Ignore cache write errors in non-lock strategies: the item was successfully
	// loaded, so we return it to the client even if caching fails.
	_ = s.cache.Put(ctx, item, ttl, jitterFraction)

	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

// loadWithLocalSingleFlight implements the in-process request coalescing strategy.
// On a cold key with 200 concurrent requests on this node, only 1 query goes to
// PostgreSQL. The other 199 goroutines block in singleflight.Group.Do() and
// receive the result when the leader's DB call completes.
//
// Across a 3-node cluster, this produces exactly 3 DB queries (1 per node).
func (s *ItemService) loadWithLocalSingleFlight(
	ctx context.Context,
	id int64,
	ttl time.Duration,
	delayMillis int64,
) (domain.CacheResponse, error) {
	// Fast path: check cache before entering the singleflight group.
	item, hit, err := s.cache.Get(ctx, id)
	if err != nil {
		return domain.CacheResponse{}, err
	}
	if hit {
		return domain.CacheResponse{Item: item, CacheHit: true}, nil
	}

	// Use the item ID as the key. All concurrent requests for item 42 share
	// the key "item:42" and coalesce onto a single execution.
	sfKey := fmt.Sprintf("item:%d", id)

	item, err = s.local.Do(sfKey, func() (domain.Item, error) {
		// Double-check the cache inside the singleflight function.
		// Reason: another goroutine might have won the election and already
		// populated the cache between our first check and us entering Do().
		if itemAfterLock, hitAfterLock, errCheck := s.cache.Get(ctx, id); errCheck == nil && hitAfterLock {
			return itemAfterLock, nil
		}

		loaded, loadErr := s.repo.FindByID(ctx, id, delayMillis)
		if loadErr != nil {
			return domain.Item{}, loadErr
		}

		_ = s.cache.Put(ctx, loaded, ttl, 0.0) // No jitter for single-flight
		return loaded, nil
	})

	if err != nil {
		return domain.CacheResponse{}, err
	}

	return domain.CacheResponse{Item: item, CacheHit: false}, nil
}

// loadWithDistributedLock implements cluster-wide request coalescing.
// Across all nodes in the 3-node cluster, exactly 1 DB query is executed,
// regardless of how many concurrent requests are in flight.
//
// The delay must be < 1000ms to ensure the follower wait window (2 * delay)
// fits within the Redis lock lease (5000ms).
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
		// cacheLookup: the function followers call while polling.
		func() (domain.Item, bool, error) {
			return s.cache.Get(ctx, id)
		},
		// loadAndCache: the function only the elected leader executes.
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
