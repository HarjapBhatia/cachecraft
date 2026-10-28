// Package servicetest contains unit tests for the service package.
//
// HOW TESTING WORKS IN GO:
//   - Test files are named with the "_test.go" suffix.
//   - Test functions must start with "Test" and accept *testing.T.
//   - Run tests with: go test ./...  (tests all packages recursively)
//   - Run a specific test: go test ./internal/service/... -run TestFollowerMaxWait
//
// We use "github.com/stretchr/testify/assert" for cleaner assertion syntax.
// Without testify, you would write:
//   if result != expected { t.Errorf("got %v, want %v", result, expected) }
// With testify:
//   assert.Equal(t, expected, result)
//
// These tests have NO external dependencies (no Redis, no PostgreSQL).
// They test only the pure mathematical logic of the timing policy.
// This is why it was worth isolating that logic in timing_policy.go.
package servicetest

import (
	"testing"
	"time"

	"cachecraft/internal/service"
	"github.com/stretchr/testify/assert"
)

// TestFollowerMaxWait verifies that the follower wait calculation correctly
// picks the larger of "2 * delayMillis" or the minimum floor of 200ms.
func TestFollowerMaxWait(t *testing.T) {
	// Table-driven test: a common Go pattern where you define a slice of test
	// cases and iterate over them. This avoids copy-pasting assertions.
	//
	// Each "case" struct has:
	//   name:     a human-readable label shown in test output on failure.
	//   delay:    the input delayMillis value.
	//   expected: the expected time.Duration output.
	cases := []struct {
		name     string
		delay    int64
		expected time.Duration
	}{
		{
			name:     "small delay uses minimum floor of 200ms",
			delay:    50,
			expected: 200 * time.Millisecond, // 2*50=100ms, less than floor 200ms
		},
		{
			name:     "delay of 100ms gives exactly the floor",
			delay:    100,
			expected: 200 * time.Millisecond, // 2*100=200ms, equal to floor
		},
		{
			name:     "large delay of 250ms exceeds floor",
			delay:    250,
			expected: 500 * time.Millisecond, // 2*250=500ms, exceeds floor 200ms
		},
		{
			name:     "zero delay uses minimum floor",
			delay:    0,
			expected: 200 * time.Millisecond,
		},
	}

	// "for _, tc := range cases" iterates over the slice.
	// "_" discards the index (we don't need it).
	// "tc" is each test case struct.
	for _, tc := range cases {
		// t.Run creates a sub-test with its own name. On failure, the output is:
		// "--- FAIL: TestFollowerMaxWait/small_delay_uses_minimum_floor_of_200ms"
		t.Run(tc.name, func(t *testing.T) {
			result := service.FollowerMaxWait(tc.delay)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestPollBase verifies the base poll interval calculation clamps correctly.
func TestPollBase(t *testing.T) {
	cases := []struct {
		name     string
		delay    int64
		expected time.Duration
	}{
		{
			name:     "zero delay clamps to minimum 5ms",
			delay:    0,
			expected: 5 * time.Millisecond,
		},
		{
			name:     "small delay clamps to minimum 5ms",
			delay:    10,
			expected: 5 * time.Millisecond, // 10/5=2ms < 5ms, clamped to 5ms
		},
		{
			name:     "medium delay 100ms gives 20ms base",
			delay:    100,
			expected: 20 * time.Millisecond, // 100/5=20ms, within [5ms, 50ms]
		},
		{
			name:     "large delay 250ms clamps to maximum 50ms",
			delay:    250,
			expected: 50 * time.Millisecond, // 250/5=50ms, exactly at ceiling
		},
		{
			name:     "very large delay clamps to maximum 50ms",
			delay:    1000,
			expected: 50 * time.Millisecond, // 1000/5=200ms > 50ms, clamped to 50ms
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := service.PollBase(tc.delay)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestJitteredPollInterval verifies that random jitter stays within [5ms, 50ms].
// Since the output is random, we run it many times and check bounds.
func TestJitteredPollInterval(t *testing.T) {
	for i := 0; i < 1000; i++ {
		result := service.JitteredPollInterval(25 * time.Millisecond)
		assert.GreaterOrEqual(t, result, 5*time.Millisecond,
			"jitter result must be >= minPollInterval")
		assert.LessOrEqual(t, result, 50*time.Millisecond,
			"jitter result must be <= maxPollInterval")
	}
}
