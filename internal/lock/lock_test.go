// Package lock_test contains unit tests for the lock package.
//
// This file tests the SingleFlightLock to verify that concurrent calls
// with the same key are collapsed into a single execution.
//
// HOW CONCURRENCY TESTING WORKS IN GO:
//   We spawn multiple goroutines simultaneously and coordinate them with a
//   sync.WaitGroup. WaitGroup works like a counter:
//     wg.Add(n)  -- expect n goroutines to finish
//     wg.Done()  -- called by each goroutine when it finishes (decrements counter)
//     wg.Wait()  -- blocks until counter reaches zero
//
//   sync.WaitGroup is the Go equivalent of Java's CountDownLatch or
//   Python's threading.Barrier for "wait for all goroutines to finish".
//
// THE KEY PROOF:
//   If singleflight works correctly, an in-flight function that takes time
//   (like a 50ms database query) should be called EXACTLY ONCE when 100
//   concurrent goroutines simultaneously call Do() with the same key.
//   All 100 callers receive the exact same result without repeating the work.
package lock_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cachecraft/internal/domain"
	"cachecraft/internal/lock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSingleFlightCollapsesConcurrentCalls verifies that 100 concurrent
// goroutines calling Do() with the same key produce exactly 1 function execution.
func TestSingleFlightCollapsesConcurrentCalls(t *testing.T) {
	sf := lock.NewSingleFlightLock()

	// executionCount tracks how many times the inner function actually ran.
	var executionCount atomic.Int64

	// expectedItem is what the inner function returns. All callers should get this.
	expectedItem := domain.Item{ID: 42, Name: "Item 42", Price: 1654, Description: "test"}

	var wg sync.WaitGroup

	// ready is a channel used to release all 100 goroutines simultaneously.
	ready := make(chan struct{})

	concurrency := 100
	results := make([]domain.Item, concurrency)
	errors := make([]error, concurrency)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()

			// Block until close(ready) broadcasts the start signal.
			<-ready

			item, err := sf.Do("item:42", func() (domain.Item, error) {
				executionCount.Add(1) // Only ONE goroutine should reach this line.
				// Simulate the real-world latency of a database query (e.g. 50ms).
				// While this leader is sleeping, all other 99 goroutines arrive,
				// find the flight in progress, and block waiting for its completion.
				time.Sleep(50 * time.Millisecond)
				return expectedItem, nil
			})

			results[i] = item
			errors[i] = err
		}()
	}

	// Release all goroutines at once by closing the channel.
	close(ready)

	// Wait for all goroutines to complete.
	wg.Wait()

	// The inner function must have run exactly once across all 100 callers.
	assert.Equal(t, int64(1), executionCount.Load(),
		"singleflight must execute the function exactly once for concurrent calls")

	// Every goroutine must have received the correct item.
	for i, err := range errors {
		require.NoError(t, err, "goroutine %d received an unexpected error", i)
		assert.Equal(t, expectedItem, results[i],
			"goroutine %d received wrong item", i)
	}
}

// TestSingleFlightAllowsSequentialCalls verifies that after a key's function
// completes, subsequent calls with the same key run the function again.
// (singleflight only deduplicates CONCURRENT calls, not all calls forever)
func TestSingleFlightAllowsSequentialCalls(t *testing.T) {
	sf := lock.NewSingleFlightLock()
	var callCount atomic.Int64

	item1 := domain.Item{ID: 1, Name: "First"}
	item2 := domain.Item{ID: 1, Name: "Second"}

	// First call
	result1, err := sf.Do("item:1", func() (domain.Item, error) {
		callCount.Add(1)
		return item1, nil
	})
	require.NoError(t, err)
	assert.Equal(t, item1, result1)

	// Second sequential call -- function must run again (first has completed)
	result2, err := sf.Do("item:1", func() (domain.Item, error) {
		callCount.Add(1)
		return item2, nil
	})
	require.NoError(t, err)
	assert.Equal(t, item2, result2)

	// Both calls ran their function (sequential, no deduplication needed)
	assert.Equal(t, int64(2), callCount.Load(),
		"sequential calls must each execute the function independently")
}
