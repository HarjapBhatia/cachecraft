package service

import (
	"math/rand/v2"
	"time"
)

const (
	minPollInterval = 5 * time.Millisecond
	maxPollInterval = 50 * time.Millisecond
	minFollowerWait = 200 * time.Millisecond
)

// FollowerMaxWait calculates the maximum duration a follower waits for a leader to populate the cache.
func FollowerMaxWait(delayMillis int64) time.Duration {
	fromDelay := time.Duration(2*delayMillis) * time.Millisecond
	if fromDelay > minFollowerWait {
		return fromDelay
	}
	return minFollowerWait
}

// PollBase calculates the base polling interval scaled against the simulated query delay.
func PollBase(delayMillis int64) time.Duration {
	base := time.Duration(delayMillis/5) * time.Millisecond
	return clampDuration(base, minPollInterval, maxPollInterval)
}

// JitteredPollInterval applies a +/-20% random variation to the base interval to prevent lock contention.
func JitteredPollInterval(base time.Duration) time.Duration {
	variation := base / 5
	if variation < time.Millisecond {
		variation = time.Millisecond
	}

	rangeSize := int64(2*variation) + 1
	offset := time.Duration(rand.Int64N(rangeSize)) - variation
	result := base + offset
	return clampDuration(result, minPollInterval, maxPollInterval)
}

func clampDuration(value, min, max time.Duration) time.Duration {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
