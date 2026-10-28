// Package domain holds the core business entities of the CacheCraft application.
//
// This file defines all the "sentinel errors" for the application.
//
// What is a sentinel error?
//   In Go, errors are just values (they implement the "error" interface).
//   A sentinel error is a specific, pre-defined error variable that you can
//   compare against using "errors.Is(err, SomeError)". This is Go's idiomatic
//   way of doing what Java does with custom Exception classes.
//
// Example usage:
//   if errors.Is(err, ErrItemNotFound) { return http.StatusNotFound }
//
// Why define them in the domain package?
//   Because these errors represent BUSINESS failures (item not found,
//   strategy not supported) -- they are not tied to any specific technology.
//   The HTTP handler can check these errors without needing to import the
//   repository or service package, which keeps the dependency graph clean.
package domain

import "errors"

// ErrItemNotFound is returned by the repository when no row exists for the
// given item ID. Equivalent to Spring Boot's ItemNotFoundException.
var ErrItemNotFound = errors.New("item not found")

// ErrUnsupportedStrategy is returned by the service when the "strategy"
// query parameter is not one of the known caching strategies.
// Equivalent to Spring Boot's UnsupportedStrategyException.
var ErrUnsupportedStrategy = errors.New("unsupported caching strategy")

// ErrInvalidLockDelay is returned when the "distributed-lock" strategy is
// used with a delay >= 1000ms, which would cause the follower wait window
// to outlast the Redis lock lease.
// Equivalent to Spring Boot's InvalidDistributedLockDelayException.
var ErrInvalidLockDelay = errors.New("delay must be less than 1000ms for distributed-lock strategy")

// ErrLockUnavailable is returned when a follower goroutine exhausts its
// entire wait window without finding the cache populated and fails to win
// the final lock election.
// Equivalent to Spring Boot's LockUnavailableException.
var ErrLockUnavailable = errors.New("distributed cache lock is unavailable")

// ErrPoolTimeout is returned when the database connection pool is exhausted
// and the context deadline expires before a connection can be acquired.
// Equivalent to catching CannotGetJdbcConnectionException in Spring Boot.
var ErrPoolTimeout = errors.New("database connection pool exhausted")
