package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_IncrWindowCountsAndDenies(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	for i := int64(1); i <= 3; i++ {
		res, err := ms.IncrWindow(ctx, "k", time.Minute, 1, 3, false, time.Minute)
		require.NoError(t, err)
		assert.True(t, res.Allowed)
		assert.Equal(t, i, res.Current)
	}

	res, err := ms.IncrWindow(ctx, "k", time.Minute, 1, 3, false, time.Minute)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, int64(3), res.Current, "denied increment must not be applied")
}

func TestMemoryStore_IncrWindowPeekIsReadOnly(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		res, err := ms.IncrWindow(ctx, "k", time.Minute, 0, 3, false, time.Minute)
		require.NoError(t, err)
		assert.True(t, res.Allowed)
		assert.Equal(t, int64(0), res.Current)
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	assert.Empty(t, ms.windows, "peeking at a missing key must not allocate state")
}

func TestMemoryStore_WindowShift(t *testing.T) {
	now := time.Unix(100, 50_000_000).UTC()
	ms := newMemoryStore(func() time.Time { return now })
	defer ms.Close()
	ctx := context.Background()
	window := 200 * time.Millisecond

	res, err := ms.IncrWindow(ctx, "k", window, 4, 100, true, time.Minute)
	require.NoError(t, err)
	first := res.WindowStart

	// Move into the next window: the old current count must appear as
	// the previous count.
	now = now.Add(window)
	res, err = ms.IncrWindow(ctx, "k", window, 1, 100, true, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Current)
	assert.Equal(t, int64(4), res.Previous)
	assert.Equal(t, first.Add(window), res.WindowStart)

	// Two more windows later, both counts are stale and must read zero.
	now = now.Add(2 * window)
	res, err = ms.IncrWindow(ctx, "k", window, 0, 100, true, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Current)
	assert.Equal(t, int64(0), res.Previous)
}

func TestMemoryStore_TakeTokensDeniedDoesNotDoubleRefill(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	// Drain a 10-token bucket refilling at 10/s.
	result, err := ms.TakeTokens(ctx, "k", 10, 10, 10, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Allowed)

	// Hammer it with denied requests. Each one observes the refill that has
	// accrued since the last admission without committing it; elapsed time must
	// never be credited twice.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, err := ms.TakeTokens(ctx, "k", 10, 10, 8, time.Minute)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	result, err = ms.TakeTokens(ctx, "k", 10, 10, 0, time.Minute)
	require.NoError(t, err)
	// ~0.3s elapsed at 10 tokens/s ≈ 3 tokens; generous upper bound well
	// under what double-crediting would produce.
	assert.Less(t, result.Tokens, 6.0)
	assert.Greater(t, result.Tokens, 1.0)
}

func TestMemoryStore_TokenPeekOnMissingKeyDoesNotAllocate(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()

	result, err := ms.TakeTokens(context.Background(), "missing", 10, 10, 0, time.Minute)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
	assert.Equal(t, 10.0, result.Tokens)

	ms.mu.RLock()
	defer ms.mu.RUnlock()
	assert.Empty(t, ms.buckets, "peeking at a missing key must not allocate state")
}

func TestMemoryStore_ImpossibleRequestsOnMissingKeysDoNotAllocate(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	window, err := ms.IncrWindow(ctx, "window", time.Minute, 11, 10, false, time.Minute)
	require.NoError(t, err)
	assert.False(t, window.Allowed)

	bucket, err := ms.TakeTokens(ctx, "bucket", 10, 1, 11, time.Minute)
	require.NoError(t, err)
	assert.False(t, bucket.Allowed)

	ms.mu.RLock()
	defer ms.mu.RUnlock()
	assert.Empty(t, ms.windows)
	assert.Empty(t, ms.buckets)
}

func TestMemoryStore_PeeksAndDenialsDoNotMutateCommittedState(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	ms := newMemoryStore(func() time.Time { return now })
	defer ms.Close()
	ctx := context.Background()

	_, err := ms.IncrWindow(ctx, "window", time.Second, 10, 10, false, 2*time.Second)
	require.NoError(t, err)
	windowEntry := ms.windows["window"]
	windowEntry.mu.Lock()
	windowCurStart, windowCur, windowPrev := windowEntry.curStart, windowEntry.cur, windowEntry.prev
	windowLastNow, windowExpiresAt := windowEntry.lastNow, windowEntry.expiresAt
	windowEntry.mu.Unlock()

	now = now.Add(100 * time.Millisecond)
	peek, err := ms.IncrWindow(ctx, "window", time.Second, 0, 10, false, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, int64(10), peek.Current)
	denied, err := ms.IncrWindow(ctx, "window", time.Second, 1, 10, false, 2*time.Second)
	require.NoError(t, err)
	assert.False(t, denied.Allowed)
	windowEntry.mu.Lock()
	assert.Equal(t, windowCurStart, windowEntry.curStart)
	assert.Equal(t, windowCur, windowEntry.cur)
	assert.Equal(t, windowPrev, windowEntry.prev)
	assert.Equal(t, windowLastNow, windowEntry.lastNow)
	assert.Equal(t, windowExpiresAt, windowEntry.expiresAt)
	windowEntry.mu.Unlock()

	now = time.Unix(200, 0).UTC()
	_, err = ms.TakeTokens(ctx, "bucket", 10, 10, 10, 2*time.Second)
	require.NoError(t, err)
	bucketEntry := ms.buckets["bucket"]
	bucketEntry.mu.Lock()
	bucketInitialized, bucketTokens := bucketEntry.initialized, bucketEntry.tokens
	bucketTS, bucketExpiresAt := bucketEntry.tsUS, bucketEntry.expiresAt
	bucketEntry.mu.Unlock()

	now = now.Add(100 * time.Millisecond)
	peekTokens, err := ms.TakeTokens(ctx, "bucket", 10, 10, 0, 2*time.Second)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, peekTokens.Tokens, 0.000001)
	deniedTokens, err := ms.TakeTokens(ctx, "bucket", 10, 10, 2, 2*time.Second)
	require.NoError(t, err)
	assert.False(t, deniedTokens.Allowed)
	assert.InDelta(t, 1.0, deniedTokens.Tokens, 0.000001)
	bucketEntry.mu.Lock()
	assert.Equal(t, bucketInitialized, bucketEntry.initialized)
	assert.Equal(t, bucketTokens, bucketEntry.tokens)
	assert.Equal(t, bucketTS, bucketEntry.tsUS)
	assert.Equal(t, bucketExpiresAt, bucketEntry.expiresAt)
	bucketEntry.mu.Unlock()
}

func TestMemoryStore_DeleteClearsState(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	_, err := ms.IncrWindow(ctx, "k", time.Minute, 5, 10, false, time.Minute)
	require.NoError(t, err)
	_, err = ms.TakeTokens(ctx, "k", 10, 1, 5, time.Minute)
	require.NoError(t, err)

	require.NoError(t, ms.Delete(ctx, "k"))

	res, err := ms.IncrWindow(ctx, "k", time.Minute, 0, 10, false, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Current)

	result, err := ms.TakeTokens(ctx, "k", 10, 1, 0, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 10.0, result.Tokens, "deleted bucket must reinitialize full")
}

func TestMemoryStore_SweepEvictsOnlyExpired(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	_, err := ms.IncrWindow(ctx, "expired", 10*time.Millisecond, 1, 10, false, 10*time.Millisecond)
	require.NoError(t, err)
	_, err = ms.IncrWindow(ctx, "live", 10*time.Millisecond, 1, 10, false, time.Hour)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	ms.sweep(time.Now().UnixMicro())

	ms.mu.RLock()
	_, expiredExists := ms.windows["expired"]
	_, liveExists := ms.windows["live"]
	ms.mu.RUnlock()
	assert.False(t, expiredExists)
	assert.True(t, liveExists)
}

func TestMemoryStore_PingAndClose(t *testing.T) {
	ms := NewMemoryStore()
	assert.NoError(t, ms.Ping(context.Background()))
	assert.NoError(t, ms.Close())
	assert.NoError(t, ms.Close(), "Close must be idempotent")
}

func TestMemoryStore_RejectsInvalidOperations(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx := context.Background()

	_, err := ms.IncrWindow(ctx, "k", 0, 1, 10, false, time.Minute)
	assert.Error(t, err)
	_, err = ms.IncrWindow(ctx, "k", 1500*time.Nanosecond, 1, 10, false, time.Minute)
	assert.Error(t, err)
	_, err = ms.IncrWindow(ctx, "k", time.Minute, -1, 10, false, time.Minute)
	assert.Error(t, err)
	_, err = ms.IncrWindow(ctx, "k", time.Minute, 1, 10, false, time.Second)
	assert.Error(t, err)
	_, err = ms.IncrWindow(ctx, "k", time.Minute, 1, 10, true, time.Minute)
	assert.Error(t, err)
	_, err = ms.TakeTokens(ctx, "k", 0, 1, 1, time.Minute)
	assert.Error(t, err)
	_, err = ms.TakeTokens(ctx, "k", 10, 0, 1, time.Minute)
	assert.Error(t, err)
	_, err = ms.TakeTokens(ctx, "k", 10, 1, -1, time.Minute)
	assert.Error(t, err)
	_, err = ms.TakeTokens(ctx, "k", 10, 1, 1, time.Nanosecond)
	assert.Error(t, err)
}

func TestMemoryStore_TTLRoundsUpToMicrosecondClock(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	ms := newMemoryStore(func() time.Time { return now })
	defer ms.Close()

	_, err := ms.TakeTokens(context.Background(), "k", 1, 2_000_000, 1, 1500*time.Nanosecond)
	require.NoError(t, err)

	ms.mu.RLock()
	expiresAt := ms.buckets["k"].expiresAt
	ms.mu.RUnlock()
	assert.Equal(t, now.UnixMicro()+2, expiresAt)
}

func TestMemoryStore_RespectsCanceledContext(t *testing.T) {
	ms := NewMemoryStore()
	defer ms.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ms.IncrWindow(ctx, "k", time.Minute, 1, 10, false, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = ms.TakeTokens(ctx, "k", 10, 1, 1, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, ms.Delete(ctx, "k"), context.Canceled)
	assert.ErrorIs(t, ms.Ping(ctx), context.Canceled)
}

func TestMemoryStore_ClampsBackwardWindowClockPerKey(t *testing.T) {
	now := time.Unix(100, 500_000_000).UTC()
	ms := newMemoryStore(func() time.Time { return now })
	defer ms.Close()
	ctx := context.Background()

	first, err := ms.IncrWindow(ctx, "k", time.Second, 3, 10, true, time.Minute)
	require.NoError(t, err)

	now = now.Add(-750 * time.Millisecond)
	second, err := ms.IncrWindow(ctx, "k", time.Second, 0, 10, true, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, first.Now, second.Now)
	assert.Equal(t, first.WindowStart, second.WindowStart)
	assert.Equal(t, int64(3), second.Current)
}

func TestMemoryStore_ClampsBackwardTokenClockPerKey(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	ms := newMemoryStore(func() time.Time { return now })
	defer ms.Close()
	ctx := context.Background()

	first, err := ms.TakeTokens(ctx, "k", 10, 10, 10, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 0.0, first.Tokens)

	now = now.Add(-time.Second)
	second, err := ms.TakeTokens(ctx, "k", 10, 10, 0, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, first.Now, second.Now)
	assert.Equal(t, 0.0, second.Tokens, "backward time must not mint or remove tokens")

	now = first.Now.Add(500 * time.Millisecond)
	third, err := ms.TakeTokens(ctx, "k", 10, 10, 0, time.Minute)
	require.NoError(t, err)
	assert.InDelta(t, 5.0, third.Tokens, 0.000001)
}
