// Package service contains the core application logic for the CacheCraft application.
//
// This file contains the LockTimingPolicy -- a set of pure mathematical
// functions that calculate timing parameters for the distributed-lock
// follower polling strategy.
//
// WHY a separate file for math?
//   These functions have zero dependencies (no Redis, no HTTP, no DB).
//   Keeping them in their own file makes them easy to unit test in isolation.
//   This mirrors the Java LockTimingPolicy class exactly.
//
// The follower polling problem:
//   When a request fails to win the Redis lock, it must wait for the leader
//   to load and cache the item. It polls Redis repeatedly, sleeping between
//   checks. We need to answer:
//     - How long should a follower wait in total?
//     - How long should it sleep between polls?
//   These functions answer those questions based on the database delay.
package service

import (
	"math/rand/v2"
	"time"
)

// Timing constants for follower polling. These mirror the values from
// LockTimingPolicy.java in the original Spring Boot implementation.
const (
	// minPollInterval is the shortest we will ever sleep between Redis checks.
	minPollInterval = 5 * time.Millisecond

	// maxPollInterval is the longest we will ever sleep between Redis checks.
	maxPollInterval = 50 * time.Millisecond

	// minFollowerWait is the minimum total time a follower will wait for the
	// leader to populate the cache, regardless of the database delay setting.
	minFollowerWait = 200 * time.Millisecond
)

// FollowerMaxWait calculates the total time a follower goroutine will wait
// before giving up and attempting its own final lock election.
//
// Formula: max(200ms, 2 * delayMillis)
//
// Rationale: The follower must wait long enough for the leader to complete
// one full database round trip. Since the delay is injected via pg_sleep,
// the leader needs at least delayMillis to finish. We double it for safety.
//
// Exported (starts with uppercase) so the lock package can call it directly.
func FollowerMaxWait(delayMillis int64) time.Duration {
	fromDelay := time.Duration(2*delayMillis) * time.Millisecond
	if fromDelay > minFollowerWait {
		return fromDelay
	}
	return minFollowerWait
}

// PollBase calculates the base polling interval for a follower.
//
// Formula: clamp(delayMillis / 5, minPollInterval, maxPollInterval)
//
// Rationale: Poll more frequently when the query is fast, less when it is slow.
// A 250ms delay gives PollBase = 50ms (maxPollInterval).
// A 100ms delay gives PollBase = 20ms.
// A 10ms delay gives PollBase = 5ms (minPollInterval).
//
// Exported so the lock package can call it.
func PollBase(delayMillis int64) time.Duration {
	base := time.Duration(delayMillis/5) * time.Millisecond
	return clampDuration(base, minPollInterval, maxPollInterval)
}

// JitteredPollInterval adds a random +/- 20% variation to the base poll
// interval, preventing all followers from hammering Redis at the exact same
// moment. The result is always clamped to [minPollInterval, maxPollInterval].
//
// Example: base=50ms, variation=10ms -> result in [40ms, 60ms] -> clamped to [40ms, 50ms]
//
// Exported so the lock package can call it.
func JitteredPollInterval(base time.Duration) time.Duration {
	variation := base / 5
	if variation < time.Millisecond {
		variation = time.Millisecond
	}

	// rand.Int64N(n) returns a random int64 in [0, n). We use it to pick a
	// value in [base - variation, base + variation].
	//
	// "math/rand/v2" (Go 1.22+) is the new standard random package. It does
	// not need explicit seeding unlike the old "math/rand" package.
	rangeSize := int64(2*variation) + 1
	offset := time.Duration(rand.Int64N(rangeSize)) - variation
	result := base + offset
	return clampDuration(result, minPollInterval, maxPollInterval)
}

// clampDuration returns value if it falls between min and max,
// min if value is below min, or max if value is above max.
// This is a generic "clamp" (bounds check) for time.Duration values.
func clampDuration(value, min, max time.Duration) time.Duration {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
