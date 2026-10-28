package lock

import (
	"fmt"

	"cachecraft/internal/domain"
	"golang.org/x/sync/singleflight"
)

// SingleFlightLock deduplicates concurrent requests for the same key in-process.
type SingleFlightLock struct {
	group singleflight.Group
}

// NewSingleFlightLock returns a new SingleFlightLock instance.
func NewSingleFlightLock() *SingleFlightLock {
	return &SingleFlightLock{}
}

// Do executes fn for key, coalescing concurrent calls onto a single in-flight execution.
func (s *SingleFlightLock) Do(key string, fn func() (domain.Item, error)) (domain.Item, error) {
	sfKey := fmt.Sprintf("sf:%s", key)

	result, err, _ := s.group.Do(sfKey, func() (any, error) {
		return fn()
	})
	if err != nil {
		return domain.Item{}, err
	}

	item, ok := result.(domain.Item)
	if !ok {
		return domain.Item{}, fmt.Errorf("singleflight returned unexpected type for key %s", key)
	}

	return item, nil
}
