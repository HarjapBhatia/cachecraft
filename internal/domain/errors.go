package domain

import "errors"

var (
	ErrItemNotFound        = errors.New("item not found")
	ErrUnsupportedStrategy = errors.New("unsupported caching strategy")
	ErrInvalidLockDelay    = errors.New("delay must be less than 1000ms for distributed-lock strategy")
	ErrLockUnavailable    = errors.New("distributed cache lock is unavailable")
	ErrPoolTimeout         = errors.New("database connection pool exhausted")
)
