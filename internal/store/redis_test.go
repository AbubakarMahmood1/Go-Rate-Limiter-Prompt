package store

// Integration tests against a real Redis. They run when REDIS_ADDR is set
// (as in CI, where a Redis service container is provided) and skip
// otherwise, so the default `go test ./...` needs no infrastructure.

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRedis(t *testing.T) *RedisStore {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		if os.Getenv("REQUIRE_REDIS") == "1" {
			t.Fatal("REQUIRE_REDIS=1 but REDIS_ADDR is not set")
		}
		t.Skip("REDIS_ADDR not set; skipping Redis integration tests")
	}
	t.Logf("running Redis integration test against %s", addr)
	rs, err := NewRedisStore(RedisConfig{Addresses: []string{addr}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rs.Close() })
	return rs
}

// uniqueKey isolates test runs from leftover state in a shared Redis.
func uniqueKey(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func TestRedisStore_IncrWindowCountsAndDenies(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()
	key := uniqueKey("win")

	for i := int64(1); i <= 3; i++ {
		res, err := rs.IncrWindow(ctx, key, time.Minute, 1, 3, false, time.Minute)
		require.NoError(t, err)
		assert.True(t, res.Allowed)
		assert.Equal(t, i, res.Current)
	}

	res, err := rs.IncrWindow(ctx, key, time.Minute, 1, 3, false, time.Minute)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, int64(3), res.Current, "denied increment must not be applied")

	// Peek stays read-only.
	res, err = rs.IncrWindow(ctx, key, time.Minute, 0, 3, false, time.Minute)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(3), res.Current)
}

func TestRedisStore_ConcurrentAdmissionIsExact(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()

	for _, weightPrevious := range []bool{false, true} {
		name := "fixed_window"
		if weightPrevious {
			name = "sliding_window"
		}
		t.Run(name, func(t *testing.T) {
			key := uniqueKey("hot-win")
			var wg sync.WaitGroup
			type outcome struct {
				allowed bool
				err     error
			}
			results := make(chan outcome, 300)
			for i := 0; i < 300; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ttl := time.Hour
					if weightPrevious {
						ttl = 2*time.Hour + time.Second
					}
					res, err := rs.IncrWindow(ctx, key, time.Hour, 1, 100, weightPrevious, ttl)
					if err != nil {
						results <- outcome{err: err}
						return
					}
					results <- outcome{allowed: res.Allowed}
				}()
			}
			wg.Wait()
			close(results)

			allowed := 0
			for result := range results {
				require.NoError(t, result.err)
				if result.allowed {
					allowed++
				}
			}
			assert.Equal(t, 100, allowed)
		})
	}

	t.Run("tokens", func(t *testing.T) {
		key := uniqueKey("hot-tb")
		var wg sync.WaitGroup
		type outcome struct {
			allowed bool
			err     error
		}
		results := make(chan outcome, 300)
		for i := 0; i < 300; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Refill over an hour is negligible during the test.
				result, err := rs.TakeTokens(ctx, key, 100, 100.0/3600, 1, time.Hour)
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
		for result := range results {
			require.NoError(t, result.err)
			if result.allowed {
				allowed++
			}
		}
		assert.Equal(t, 100, allowed)
	})
}

func TestRedisStore_TakeTokensRefills(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()
	key := uniqueKey("tb")

	result, err := rs.TakeTokens(ctx, key, 10, 10, 10, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Allowed)
	assert.InDelta(t, 0, result.Tokens, 0.1)
	assert.WithinDuration(t, time.Now(), result.Now, 5*time.Second)

	time.Sleep(500 * time.Millisecond)

	result, err = rs.TakeTokens(ctx, key, 10, 10, 3, time.Minute)
	require.NoError(t, err)
	assert.True(t, result.Allowed, "≈5 tokens should have refilled, got %f", result.Tokens)
}

func TestRedisStore_StateExpires(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()
	key := uniqueKey("ttl")

	_, err := rs.IncrWindow(ctx, key, time.Minute, 1, 10, false, time.Minute)
	require.NoError(t, err)

	ttl, err := rs.client.TTL(ctx, "rl:"+key).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0), "counter keys must carry a TTL")
	assert.LessOrEqual(t, ttl, time.Minute+time.Second)
}

func TestRedisStore_PeeksAndImpossibleRequestsDoNotCreateKeys(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()

	windowPeek := uniqueKey("window-peek")
	result, err := rs.IncrWindow(ctx, windowPeek, time.Minute, 0, 10, false, time.Minute)
	require.NoError(t, err)
	assert.True(t, result.Allowed)

	windowImpossible := uniqueKey("window-impossible")
	result, err = rs.IncrWindow(ctx, windowImpossible, time.Minute, 11, 10, false, time.Minute)
	require.NoError(t, err)
	assert.False(t, result.Allowed)

	tokenPeek := uniqueKey("token-peek")
	tokens, err := rs.TakeTokens(ctx, tokenPeek, 10, 1, 0, time.Minute)
	require.NoError(t, err)
	assert.True(t, tokens.Allowed)

	tokenImpossible := uniqueKey("token-impossible")
	tokens, err = rs.TakeTokens(ctx, tokenImpossible, 10, 1, 11, time.Minute)
	require.NoError(t, err)
	assert.False(t, tokens.Allowed)

	for _, key := range []string{windowPeek, windowImpossible, tokenPeek, tokenImpossible} {
		exists, err := rs.client.Exists(ctx, "rl:"+key).Result()
		require.NoError(t, err)
		assert.Zero(t, exists, "%s should remain allocation-free", key)
	}
}

func TestRedisStore_PeeksAndDenialsDoNotMutateCommittedState(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()

	windowKey := uniqueKey("window-observe")
	_, err := rs.IncrWindow(ctx, windowKey, time.Minute, 10, 10, false, time.Minute)
	require.NoError(t, err)
	windowBefore, err := rs.client.HGetAll(ctx, "rl:"+windowKey).Result()
	require.NoError(t, err)
	_, err = rs.IncrWindow(ctx, windowKey, time.Minute, 0, 10, false, time.Minute)
	require.NoError(t, err)
	deniedWindow, err := rs.IncrWindow(ctx, windowKey, time.Minute, 1, 10, false, time.Minute)
	require.NoError(t, err)
	assert.False(t, deniedWindow.Allowed)
	windowAfter, err := rs.client.HGetAll(ctx, "rl:"+windowKey).Result()
	require.NoError(t, err)
	assert.Equal(t, windowBefore, windowAfter)

	tokenKey := uniqueKey("token-observe")
	refill := 10.0 / time.Hour.Seconds()
	_, err = rs.TakeTokens(ctx, tokenKey, 10, refill, 10, time.Hour)
	require.NoError(t, err)
	tokenBefore, err := rs.client.HGetAll(ctx, "rl:"+tokenKey).Result()
	require.NoError(t, err)
	_, err = rs.TakeTokens(ctx, tokenKey, 10, refill, 0, time.Hour)
	require.NoError(t, err)
	deniedTokens, err := rs.TakeTokens(ctx, tokenKey, 10, refill, 1, time.Hour)
	require.NoError(t, err)
	assert.False(t, deniedTokens.Allowed)
	tokenAfter, err := rs.client.HGetAll(ctx, "rl:"+tokenKey).Result()
	require.NoError(t, err)
	assert.Equal(t, tokenBefore, tokenAfter)
}

func TestRedisStore_DeleteClearsState(t *testing.T) {
	rs := newRedis(t)
	ctx := context.Background()
	key := uniqueKey("del")

	_, err := rs.TakeTokens(ctx, key, 10, 1, 10, time.Hour)
	require.NoError(t, err)
	require.NoError(t, rs.Delete(ctx, key))

	result, err := rs.TakeTokens(ctx, key, 10, 1, 0, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 10.0, result.Tokens, "deleted bucket must reinitialize full")
}

func TestNewRedisStore_RejectsInvalidConfigWithoutDialing(t *testing.T) {
	_, err := NewRedisStore(RedisConfig{})
	assert.Error(t, err)
	_, err = NewRedisStore(RedisConfig{Addresses: []string{""}})
	assert.Error(t, err)
	_, err = NewRedisStore(RedisConfig{Addresses: []string{"localhost:6379"}, DB: -1})
	assert.Error(t, err)
	_, err = NewRedisStore(RedisConfig{Addresses: []string{"redis-a:6379", "redis-b:6379"}})
	assert.Error(t, err)
	_, err = NewRedisStore(RedisConfig{Addresses: []string{"redis-a:6379,redis-b:6379"}})
	assert.Error(t, err)
	_, err = NewRedisStore(RedisConfig{Addresses: []string{"localhost:6379"}, PoolSize: -1})
	assert.Error(t, err)
}

func TestRedisStore_RespectsCanceledContext(t *testing.T) {
	rs := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rs.IncrWindow(ctx, uniqueKey("cancel-window"), time.Minute, 1, 10, false, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = rs.TakeTokens(ctx, uniqueKey("cancel-token"), 10, 10, 1, time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, rs.Delete(ctx, uniqueKey("cancel-delete")), context.Canceled)
	assert.ErrorIs(t, rs.Ping(ctx), context.Canceled)
}
