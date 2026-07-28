package algorithms_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/internal/algorithms"
	"github.com/AbubakarMahmood/go-rate-limiter/internal/store"
	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redisAlgorithmStore(t *testing.T) *store.RedisStore {
	t.Helper()
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		if os.Getenv("REQUIRE_REDIS") == "1" {
			t.Fatal("REQUIRE_REDIS=1 but REDIS_ADDR is not set")
		}
		t.Skip("REDIS_ADDR not set; skipping Redis algorithm integration tests")
	}
	t.Logf("running Redis algorithm integration test against %s", address)
	backend, err := store.NewRedisStore(store.RedisConfig{Addresses: []string{address}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func redisAlgorithmKey(testName, algorithm string) string {
	return fmt.Sprintf("algorithm-%s-%s-%d", testName, algorithm, time.Now().UnixNano())
}

func TestRedisAlgorithms_ConcurrentAdmissionIsExact(t *testing.T) {
	backend := redisAlgorithmStore(t)
	config := limiter.Config{Limit: 100, Window: time.Hour, Burst: 100}
	factories := map[string]func() limiter.RateLimiter{
		"token_bucket": func() limiter.RateLimiter { return algorithms.NewTokenBucket(backend, config) },
		"sliding_window": func() limiter.RateLimiter {
			return algorithms.NewSlidingWindowCounter(backend, config)
		},
		"fixed_window": func() limiter.RateLimiter { return algorithms.NewFixedWindowCounter(backend, config) },
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			instance := factory()
			key := redisAlgorithmKey(t.Name(), name)
			results := make(chan bool, 300)
			errors := make(chan error, 300)
			var group sync.WaitGroup
			for i := 0; i < 300; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					result, err := instance.Allow(context.Background(), key)
					if err != nil {
						errors <- err
						return
					}
					results <- result.Allowed
				}()
			}
			group.Wait()
			close(results)
			close(errors)

			for err := range errors {
				require.NoError(t, err)
			}
			allowed := 0
			for result := range results {
				if result {
					allowed++
				}
			}
			assert.Equal(t, 100, allowed)
		})
	}
}

func TestRedisAlgorithms_DeniedBatchConsumesNothing(t *testing.T) {
	backend := redisAlgorithmStore(t)
	config := limiter.Config{Limit: 10, Window: time.Hour, Burst: 10}
	factories := map[string]func() limiter.RateLimiter{
		"token_bucket": func() limiter.RateLimiter { return algorithms.NewTokenBucket(backend, config) },
		"sliding_window": func() limiter.RateLimiter {
			return algorithms.NewSlidingWindowCounter(backend, config)
		},
		"fixed_window": func() limiter.RateLimiter { return algorithms.NewFixedWindowCounter(backend, config) },
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			instance := factory()
			key := redisAlgorithmKey(t.Name(), name)

			first, err := instance.AllowN(context.Background(), key, 6)
			require.NoError(t, err)
			require.True(t, first.Allowed)

			denied, err := instance.AllowN(context.Background(), key, 5)
			require.NoError(t, err)
			require.False(t, denied.Allowed)

			last, err := instance.AllowN(context.Background(), key, 4)
			require.NoError(t, err)
			assert.True(t, last.Allowed, "the denied five-permit batch must not consume state")
			assert.Equal(t, 0, last.Remaining)
		})
	}
}

func TestRedisAlgorithms_MatchMemoryVisibleDecisions(t *testing.T) {
	redisBackend := redisAlgorithmStore(t)
	memoryBackend := store.NewMemoryStore()
	t.Cleanup(func() { _ = memoryBackend.Close() })

	config := limiter.Config{Limit: 10, Window: time.Hour, Burst: 10}
	factories := map[string]func(limiter.Store) limiter.RateLimiter{
		"token_bucket": func(backend limiter.Store) limiter.RateLimiter {
			return algorithms.NewTokenBucket(backend, config)
		},
		"sliding_window": func(backend limiter.Store) limiter.RateLimiter {
			return algorithms.NewSlidingWindowCounter(backend, config)
		},
		"fixed_window": func(backend limiter.Store) limiter.RateLimiter {
			return algorithms.NewFixedWindowCounter(backend, config)
		},
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			memoryLimiter := factory(memoryBackend)
			redisLimiter := factory(redisBackend)
			memoryKey := redisAlgorithmKey(t.Name(), name+"-memory")
			redisKey := redisAlgorithmKey(t.Name(), name+"-redis")

			for _, count := range []int{6, 5, 4, 1} {
				memoryResult, memoryErr := memoryLimiter.AllowN(context.Background(), memoryKey, count)
				redisResult, redisErr := redisLimiter.AllowN(context.Background(), redisKey, count)
				require.NoError(t, memoryErr)
				require.NoError(t, redisErr)
				assertVisibleParity(t, memoryResult, redisResult)
			}

			memoryStatus, memoryErr := memoryLimiter.Peek(context.Background(), memoryKey)
			redisStatus, redisErr := redisLimiter.Peek(context.Background(), redisKey)
			require.NoError(t, memoryErr)
			require.NoError(t, redisErr)
			assertVisibleParity(t, memoryStatus, redisStatus)

			require.NoError(t, memoryLimiter.Reset(context.Background(), memoryKey))
			require.NoError(t, redisLimiter.Reset(context.Background(), redisKey))
			memoryStatus, memoryErr = memoryLimiter.Peek(context.Background(), memoryKey)
			redisStatus, redisErr = redisLimiter.Peek(context.Background(), redisKey)
			require.NoError(t, memoryErr)
			require.NoError(t, redisErr)
			assertVisibleParity(t, memoryStatus, redisStatus)
		})
	}
}

func assertVisibleParity(t *testing.T, memoryResult, redisResult *limiter.Result) {
	t.Helper()
	require.NotNil(t, memoryResult)
	require.NotNil(t, redisResult)
	assert.Equal(t, memoryResult.Allowed, redisResult.Allowed)
	assert.Equal(t, memoryResult.Limit, redisResult.Limit)
	assert.Equal(t, memoryResult.Remaining, redisResult.Remaining)
	assert.Equal(t, memoryResult.RetryAfter > 0, redisResult.RetryAfter > 0)
}
