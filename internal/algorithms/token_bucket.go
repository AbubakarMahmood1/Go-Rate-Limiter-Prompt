package algorithms

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
)

const maxDuration = time.Duration(1<<63 - 1)

// TokenBucket refills a bucket of capacity tokens at a constant rate; each
// request consumes tokens. It shapes traffic to a smooth average rate while
// letting clients spend accumulated tokens in bursts up to the capacity.
type TokenBucket struct {
	store        limiter.Store
	capacity     int
	refillPerSec float64
	ttl          time.Duration
}

// NewTokenBucket creates a token-bucket limiter that refills config.Limit
// tokens per config.Window, holding at most config.Burst tokens
// (config.Limit when Burst is zero).
func NewTokenBucket(store limiter.Store, config limiter.Config) *TokenBucket {
	capacity := config.Burst
	if capacity <= 0 {
		capacity = config.Limit
	}
	refillPerSec := float64(config.Limit) / config.Window.Seconds()

	refillDuration := fullRefillDuration(capacity, config.Limit, config.Window)
	ttl := refillDuration
	if ttl <= maxDuration-time.Second {
		ttl += time.Second
	}

	return &TokenBucket{
		store:        store,
		capacity:     capacity,
		refillPerSec: refillPerSec,
		// An evicted bucket re-initializes full, so state must live for at
		// least one complete refill plus slack.
		ttl: ttl,
	}
}

// Allow implements limiter.RateLimiter.
func (tb *TokenBucket) Allow(ctx context.Context, key string) (*limiter.Result, error) {
	return tb.AllowN(ctx, key, 1)
}

// AllowN implements limiter.RateLimiter.
func (tb *TokenBucket) AllowN(ctx context.Context, key string, n int) (*limiter.Result, error) {
	if n <= 0 {
		return nil, limiter.ErrInvalidCount
	}
	if n > tb.capacity {
		return nil, limiter.ErrExceedsLimit
	}

	state, err := tb.store.TakeTokens(ctx, "tb:"+key, float64(tb.capacity), tb.refillPerSec, float64(n), tb.ttl)
	if err != nil {
		return nil, fmt.Errorf("token bucket: %w", err)
	}
	return tb.result(state, n), nil
}

// Peek implements limiter.RateLimiter.
func (tb *TokenBucket) Peek(ctx context.Context, key string) (*limiter.Result, error) {
	state, err := tb.store.TakeTokens(ctx, "tb:"+key, float64(tb.capacity), tb.refillPerSec, 0, tb.ttl)
	if err != nil {
		return nil, fmt.Errorf("token bucket: %w", err)
	}
	state.Allowed = state.Tokens >= 1
	return tb.result(state, 1), nil
}

// Reset implements limiter.RateLimiter.
func (tb *TokenBucket) Reset(ctx context.Context, key string) error {
	return tb.store.Delete(ctx, "tb:"+key)
}

func (tb *TokenBucket) result(state *limiter.TokenResult, n int) *limiter.Result {
	remaining := int(math.Floor(state.Tokens))
	if remaining < 0 {
		remaining = 0
	}
	if remaining > tb.capacity {
		remaining = tb.capacity
	}

	untilFull := durationFromSecondsCeil((float64(tb.capacity) - state.Tokens) / tb.refillPerSec)
	r := &limiter.Result{
		Allowed:   state.Allowed,
		Limit:     tb.capacity,
		Remaining: remaining,
		ResetAt:   state.Now.Add(untilFull),
	}
	if !state.Allowed {
		r.RetryAfter = durationFromSecondsCeil((float64(n) - state.Tokens) / tb.refillPerSec)
	}
	return r
}

// durationFromSecondsCeil converts fractional seconds to a duration while
// rounding up to the stores' microsecond clock resolution. A retry/reset
// timestamp must never land inside the same backend clock tick.
func durationFromSecondsCeil(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	micros := math.Ceil(seconds * 1e6)
	maxMicros := float64(maxDuration / time.Microsecond)
	if micros >= maxMicros {
		return maxDuration
	}
	return time.Duration(micros) * time.Microsecond
}

// fullRefillDuration computes ceil(window*capacity/limit) without overflowing
// time.Duration. Configuration validation rejects values that would saturate;
// the defensive cap keeps direct internal callers safe as well.
func fullRefillDuration(capacity, limit int, window time.Duration) time.Duration {
	if capacity <= 0 || limit <= 0 || window <= 0 {
		return 0
	}

	numerator := new(big.Int).Mul(big.NewInt(int64(window)), big.NewInt(int64(capacity)))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, big.NewInt(int64(limit)), remainder)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Int64() > int64(maxDuration-time.Second) {
		return maxDuration - time.Second
	}
	return time.Duration(quotient.Int64())
}
