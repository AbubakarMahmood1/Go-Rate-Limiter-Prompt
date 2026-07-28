package algorithms_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/internal/algorithms"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/store"
	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMemory(t *testing.T) *store.MemoryStore {
	t.Helper()
	s := store.NewMemoryStore()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTokenBucket_AllowUpToCapacity(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Hour, Burst: 10})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		res, err := tb.Allow(ctx, "k")
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
		assert.Equal(t, 10, res.Limit)
		assert.Equal(t, 9-i, res.Remaining)
	}

	res, err := tb.Allow(ctx, "k")
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
}

func TestTokenBucket_Refill(t *testing.T) {
	// 10 tokens per second.
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Second, Burst: 10})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_, err := tb.Allow(ctx, "k")
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	res, err := tb.Allow(ctx, "k")
	require.NoError(t, err)
	assert.True(t, res.Allowed, "should be allowed after refill")
	assert.Greater(t, res.Remaining, 0)
}

func TestTokenBucket_SubSecondRetryAfter(t *testing.T) {
	// The refill rate is 10/s, so an empty bucket is one token short for
	// only ~100ms. A whole-second RetryAfter here would indicate the
	// duration math truncates.
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Second, Burst: 10})
	ctx := context.Background()

	_, err := tb.AllowN(ctx, "k", 10)
	require.NoError(t, err)

	res, err := tb.Allow(ctx, "k")
	require.NoError(t, err)
	require.False(t, res.Allowed)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
	assert.Less(t, res.RetryAfter, 500*time.Millisecond)
}

func TestTokenBucket_AllowNAllOrNothing(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Hour, Burst: 10})
	ctx := context.Background()

	res, err := tb.AllowN(ctx, "k", 5)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 5, res.Remaining)

	// A denied request must not consume anything.
	res, err = tb.AllowN(ctx, "k", 6)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, 5, res.Remaining)

	res, err = tb.AllowN(ctx, "k", 5)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
}

func TestAlgorithms_InvalidCounts(t *testing.T) {
	ctx := context.Background()
	cfg := limiter.Config{Limit: 10, Window: time.Hour, Burst: 10}
	limiters := map[string]limiter.RateLimiter{
		"token_bucket":   algorithms.NewTokenBucket(newMemory(t), cfg),
		"sliding_window": algorithms.NewSlidingWindowCounter(newMemory(t), cfg),
		"fixed_window":   algorithms.NewFixedWindowCounter(newMemory(t), cfg),
	}

	for name, instance := range limiters {
		t.Run(name, func(t *testing.T) {
			_, err := instance.AllowN(ctx, "k", -1)
			assert.ErrorIs(t, err, limiter.ErrInvalidCount)
			_, err = instance.AllowN(ctx, "k", 0)
			assert.ErrorIs(t, err, limiter.ErrInvalidCount)
			_, err = instance.AllowN(ctx, "k", 11)
			assert.ErrorIs(t, err, limiter.ErrExceedsLimit)
		})
	}
}

func TestAlgorithms_InvalidWindowFailsBeforeDecision(t *testing.T) {
	ctx := context.Background()
	cfg := limiter.Config{Limit: 10, Window: 0, Burst: 10}
	limiters := map[string]limiter.RateLimiter{
		"token_bucket":   algorithms.NewTokenBucket(newMemory(t), cfg),
		"sliding_window": algorithms.NewSlidingWindowCounter(newMemory(t), cfg),
		"fixed_window":   algorithms.NewFixedWindowCounter(newMemory(t), cfg),
	}
	for name, instance := range limiters {
		t.Run(name, func(t *testing.T) {
			_, err := instance.Allow(ctx, "k")
			assert.ErrorIs(t, err, limiter.ErrInvalidStoreOperation)
		})
	}
}

type fixedDecisionStore struct {
	window *limiter.WindowResult
	tokens *limiter.TokenResult
}

func (s *fixedDecisionStore) IncrWindow(context.Context, string, time.Duration, int64, int64, bool, time.Duration) (*limiter.WindowResult, error) {
	copy := *s.window
	return &copy, nil
}

func (s *fixedDecisionStore) TakeTokens(context.Context, string, float64, float64, float64, time.Duration) (*limiter.TokenResult, error) {
	copy := *s.tokens
	return &copy, nil
}

func (*fixedDecisionStore) Delete(context.Context, string) error { return nil }
func (*fixedDecisionStore) Ping(context.Context) error           { return nil }
func (*fixedDecisionStore) Close() error                         { return nil }

func TestTokenBucket_ResetAtUsesStoreClock(t *testing.T) {
	backendNow := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	store := &fixedDecisionStore{tokens: &limiter.TokenResult{
		Allowed: true,
		Tokens:  5,
		Now:     backendNow,
	}}
	tb := algorithms.NewTokenBucket(store, limiter.Config{Limit: 10, Window: time.Second, Burst: 10})

	result, err := tb.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, backendNow.Add(500*time.Millisecond), result.ResetAt)
}

func TestTokenBucket_RetryRoundsUpToStoreClockTick(t *testing.T) {
	backendNow := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	store := &fixedDecisionStore{tokens: &limiter.TokenResult{
		Allowed: false,
		Tokens:  0.9999996,
		Now:     backendNow,
	}}
	tb := algorithms.NewTokenBucket(store, limiter.Config{Limit: 1, Window: time.Second, Burst: 1})

	result, err := tb.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, time.Microsecond, result.RetryAfter)
}

func TestSlidingWindow_RetryAfterCanCrossBoundary(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	store := &fixedDecisionStore{window: &limiter.WindowResult{
		Allowed:     false,
		Current:     10,
		Previous:    0,
		WindowStart: start,
		Now:         start.Add(900 * time.Millisecond),
	}}
	sliding := algorithms.NewSlidingWindowCounter(store, limiter.Config{Limit: 10, Window: time.Second})

	result, err := sliding.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, 200*time.Millisecond, result.RetryAfter,
		"the full current window becomes previous at the boundary and needs 10%% of the next window to decay")
	assert.Equal(t, start.Add(2*time.Second), result.ResetAt)
}

func TestSlidingWindow_RetryAfterWithinCurrentWindow(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	store := &fixedDecisionStore{window: &limiter.WindowResult{
		Allowed:     false,
		Current:     5,
		Previous:    10,
		WindowStart: start,
		Now:         start.Add(500 * time.Millisecond),
	}}
	sliding := algorithms.NewSlidingWindowCounter(store, limiter.Config{Limit: 10, Window: time.Second})

	result, err := sliding.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, 100*time.Millisecond, result.RetryAfter)
}

func TestSlidingWindow_RetryAfterIsFirstSafeMicrosecond(t *testing.T) {
	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	state := &limiter.WindowResult{
		Allowed:     false,
		Current:     7,
		Previous:    9,
		WindowStart: start,
		Now:         start.Add(123456 * time.Microsecond),
	}
	store := &fixedDecisionStore{window: state}
	sliding := algorithms.NewSlidingWindowCounter(store, limiter.Config{Limit: 10, Window: time.Second})

	result, err := sliding.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.Greater(t, result.RetryAfter, time.Duration(0))

	targetOffset := state.Now.Sub(start) + result.RetryAfter
	assert.True(t, weightedSlidingAllows(state, 1, 10, time.Second, targetOffset))
	assert.False(t, weightedSlidingAllows(state, 1, 10, time.Second, targetOffset-time.Microsecond),
		"one microsecond earlier must still be denied")
}

func weightedSlidingAllows(state *limiter.WindowResult, n, limit int, window, offset time.Duration) bool {
	weighted := 0.0
	switch {
	case offset < window:
		weighted = float64(state.Current) + float64(state.Previous)*(1-float64(offset)/float64(window))
	case offset < 2*window:
		weighted = float64(state.Current) * (1 - float64(offset-window)/float64(window))
	}
	return weighted+float64(n) <= float64(limit)
}

func TestTokenBucket_BurstDefaultsToLimit(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 7, Window: time.Hour})
	ctx := context.Background()

	res, err := tb.Peek(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, 7, res.Limit)
	assert.Equal(t, 7, res.Remaining)
}

func TestTokenBucket_PeekDoesNotConsume(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Hour, Burst: 10})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		res, err := tb.Peek(ctx, "k")
		require.NoError(t, err)
		assert.True(t, res.Allowed)
		assert.Equal(t, 10, res.Remaining)
	}

	res, err := tb.Allow(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, 9, res.Remaining)
}

func TestTokenBucket_Reset(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Hour, Burst: 10})
	ctx := context.Background()

	_, err := tb.AllowN(ctx, "k", 10)
	require.NoError(t, err)
	require.NoError(t, tb.Reset(ctx, "k"))

	res, err := tb.Allow(ctx, "k")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 9, res.Remaining)
}

func TestSlidingWindow_AllowUpToLimit(t *testing.T) {
	swc := algorithms.NewSlidingWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Minute})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		res, err := swc.Allow(ctx, "k")
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
	}

	res, err := swc.Allow(ctx, "k")
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
}

func TestSlidingWindow_AllowNConsumesN(t *testing.T) {
	swc := algorithms.NewSlidingWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Minute})
	ctx := context.Background()

	res, err := swc.AllowN(ctx, "k", 4)
	require.NoError(t, err)
	assert.True(t, res.Allowed)

	res, err = swc.AllowN(ctx, "k", 4)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 2, res.Remaining)

	res, err = swc.AllowN(ctx, "k", 3)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "9th-11th permits must be denied")

	res, err = swc.AllowN(ctx, "k", 2)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
}

func TestSlidingWindow_SlidesOpenOverTime(t *testing.T) {
	swc := algorithms.NewSlidingWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Second})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := swc.Allow(ctx, "k")
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	res, err := swc.Allow(ctx, "k")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

func TestFixedWindow_AllowUpToLimit(t *testing.T) {
	fwc := algorithms.NewFixedWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Minute})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		res, err := fwc.Allow(ctx, "k")
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
		assert.Equal(t, 9-i, res.Remaining)
	}

	res, err := fwc.Allow(ctx, "k")
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
}

func TestFixedWindow_AllowNConsumesN(t *testing.T) {
	fwc := algorithms.NewFixedWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Minute})
	ctx := context.Background()

	res, err := fwc.AllowN(ctx, "k", 7)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 3, res.Remaining)

	// All-or-nothing: 4 > 3 remaining, and the denial must not consume.
	res, err = fwc.AllowN(ctx, "k", 4)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, 3, res.Remaining)

	res, err = fwc.AllowN(ctx, "k", 3)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
}

func TestFixedWindow_WindowRollsOver(t *testing.T) {
	fwc := algorithms.NewFixedWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: 500 * time.Millisecond})
	ctx := context.Background()

	_, err := fwc.AllowN(ctx, "k", 10)
	require.NoError(t, err)

	res, err := fwc.Allow(ctx, "k")
	require.NoError(t, err)
	require.False(t, res.Allowed)

	time.Sleep(600 * time.Millisecond)

	res, err = fwc.Allow(ctx, "k")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 9, res.Remaining)
}

func TestFixedWindow_PeekDoesNotConsume(t *testing.T) {
	fwc := algorithms.NewFixedWindowCounter(newMemory(t), limiter.Config{Limit: 10, Window: time.Minute})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		res, err := fwc.Peek(ctx, "k")
		require.NoError(t, err)
		assert.True(t, res.Allowed)
		assert.Equal(t, 10, res.Remaining)
	}

	res, err := fwc.Allow(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, 9, res.Remaining)
}

// TestAlgorithmsDoNotShareState guards against different algorithms reading
// each other's counters for the same client key.
func TestAlgorithmsDoNotShareState(t *testing.T) {
	s := newMemory(t)
	cfg := limiter.Config{Limit: 5, Window: time.Minute, Burst: 5}
	ctx := context.Background()

	fixed := algorithms.NewFixedWindowCounter(s, cfg)
	sliding := algorithms.NewSlidingWindowCounter(s, cfg)
	bucket := algorithms.NewTokenBucket(s, cfg)

	_, err := fixed.AllowN(ctx, "k", 5)
	require.NoError(t, err)

	res, err := sliding.AllowN(ctx, "k", 5)
	require.NoError(t, err)
	assert.True(t, res.Allowed, "sliding window must not see fixed window's count")

	res, err = bucket.AllowN(ctx, "k", 5)
	require.NoError(t, err)
	assert.True(t, res.Allowed, "token bucket must not see window counts")
}

// TestConcurrentAdmissionIsExact is the atomicity contract: under
// contention, exactly limit permits may be granted, never more.
func TestConcurrentAdmissionIsExact(t *testing.T) {
	ctx := context.Background()

	constructors := map[string]func() limiter.RateLimiter{
		// The hour-long window makes token refill negligible during the test.
		"token_bucket": func() limiter.RateLimiter {
			return algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 100, Window: time.Hour, Burst: 100})
		},
		"sliding_window": func() limiter.RateLimiter {
			return algorithms.NewSlidingWindowCounter(newMemory(t), limiter.Config{Limit: 100, Window: time.Hour})
		},
		"fixed_window": func() limiter.RateLimiter {
			return algorithms.NewFixedWindowCounter(newMemory(t), limiter.Config{Limit: 100, Window: time.Hour})
		},
	}

	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			instance := construct()
			type outcome struct {
				allowed bool
				err     error
			}
			var wg sync.WaitGroup
			results := make(chan outcome, 300)
			for i := 0; i < 300; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := instance.Allow(ctx, "hot-key")
					if err != nil {
						results <- outcome{err: err}
						return
					}
					results <- outcome{allowed: result.Allowed}
				}()
			}
			wg.Wait()
			close(results)

			allowed := 0
			seen := 0
			for result := range results {
				require.NoError(t, result.err)
				seen++
				if result.allowed {
					allowed++
				}
			}
			assert.Equal(t, 300, seen)
			assert.Equal(t, 100, allowed)
		})
	}
}

func TestMultipleKeysAreIndependent(t *testing.T) {
	tb := algorithms.NewTokenBucket(newMemory(t), limiter.Config{Limit: 10, Window: time.Hour, Burst: 10})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		res1, err := tb.Allow(ctx, "key1")
		require.NoError(t, err)
		res2, err := tb.Allow(ctx, "key2")
		require.NoError(t, err)
		assert.True(t, res1.Allowed)
		assert.True(t, res2.Allowed)
	}

	res1, err := tb.Allow(ctx, "key1")
	require.NoError(t, err)
	res2, err := tb.Allow(ctx, "key2")
	require.NoError(t, err)
	assert.False(t, res1.Allowed)
	assert.False(t, res2.Allowed)
}

func TestEveryAlgorithm_PeekAndResetPreserveContract(t *testing.T) {
	ctx := context.Background()
	cfg := limiter.Config{Limit: 5, Window: time.Hour, Burst: 5}
	constructors := map[string]func(limiter.Store) limiter.RateLimiter{
		"token_bucket":   func(s limiter.Store) limiter.RateLimiter { return algorithms.NewTokenBucket(s, cfg) },
		"sliding_window": func(s limiter.Store) limiter.RateLimiter { return algorithms.NewSlidingWindowCounter(s, cfg) },
		"fixed_window":   func(s limiter.Store) limiter.RateLimiter { return algorithms.NewFixedWindowCounter(s, cfg) },
	}

	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			instance := construct(newMemory(t))
			consumed, err := instance.AllowN(ctx, "k", 5)
			require.NoError(t, err)
			require.True(t, consumed.Allowed)

			for i := 0; i < 3; i++ {
				state, err := instance.Peek(ctx, "k")
				require.NoError(t, err)
				assert.False(t, state.Allowed)
				assert.Equal(t, 0, state.Remaining)
			}

			require.NoError(t, instance.Reset(ctx, "k"))
			state, err := instance.Peek(ctx, "k")
			require.NoError(t, err)
			assert.True(t, state.Allowed)
			assert.Equal(t, 5, state.Remaining)
		})
	}
}
