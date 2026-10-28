// Package cache contains the Redis adapter for the CacheCraft application.
//
// This file tests the TTL jitter calculations in isolation.
// Because it is in "package cache", it has direct access to the unexported
// effectiveTTLWithJitter function without needing any test exports.
package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestEffectiveTTLNoJitter verifies that zero jitter returns the exact base TTL.
func TestEffectiveTTLNoJitter(t *testing.T) {
	base := 5 * time.Second
	result := effectiveTTLWithJitter(base, 0.0)
	assert.Equal(t, base, result, "zero jitter should return base TTL unchanged")
}

// TestEffectiveTTLWithJitterBounds verifies that jittered TTL stays within bounds.
func TestEffectiveTTLWithJitterBounds(t *testing.T) {
	base := 5 * time.Second
	jitter := 0.10 // +/- 10%

	lower := time.Duration(float64(base) * 0.90)
	upper := time.Duration(float64(base) * 1.10)

	for i := 0; i < 5000; i++ {
		result := effectiveTTLWithJitter(base, jitter)
		assert.GreaterOrEqual(t, result, lower,
			"jittered TTL must be >= base * (1 - jitter)")
		assert.LessOrEqual(t, result, upper,
			"jittered TTL must be <= base * (1 + jitter)")
	}
}

// TestEffectiveTTLMinimumFloor verifies that even extreme negative jitter
// never produces a zero or sub-millisecond TTL.
func TestEffectiveTTLMinimumFloor(t *testing.T) {
	base := time.Millisecond
	jitter := 0.99 // 99% jitter

	for i := 0; i < 1000; i++ {
		result := effectiveTTLWithJitter(base, jitter)
		assert.GreaterOrEqual(t, result, minimumTTL,
			"TTL must never be less than minimumTTL (1ms)")
	}
}
