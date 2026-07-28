package store

import (
	"fmt"
	"math"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
)

// Redis Lua uses IEEE-754 doubles. Keeping every counter at or below 2^52-1
// keeps fixed-window current+n and sliding-window weighted+n below 2^53,
// where integer values remain exact.
const maxExactCounter int64 = 1<<52 - 1

const maxStoreDuration = time.Duration(1<<63 - 1)

func validateWindowOperation(window time.Duration, n, limit int64, weightPrev bool, ttl time.Duration) error {
	switch {
	case window < time.Microsecond:
		return fmt.Errorf("%w: window must be at least 1µs", limiter.ErrInvalidStoreOperation)
	case window%time.Microsecond != 0:
		return fmt.Errorf("%w: window must be a whole number of microseconds", limiter.ErrInvalidStoreOperation)
	case n < 0:
		return fmt.Errorf("%w: increment must not be negative", limiter.ErrInvalidStoreOperation)
	case limit <= 0 || limit > maxExactCounter:
		return fmt.Errorf("%w: limit must be in [1, %d]", limiter.ErrInvalidStoreOperation, maxExactCounter)
	case n > maxExactCounter:
		return fmt.Errorf("%w: increment exceeds %d", limiter.ErrInvalidStoreOperation, maxExactCounter)
	}

	minimumTTL := window
	if weightPrev {
		if window > maxStoreDuration/2 {
			return fmt.Errorf("%w: sliding-window duration is too large", limiter.ErrInvalidStoreOperation)
		}
		minimumTTL = 2 * window
	}
	if ttl < minimumTTL {
		return fmt.Errorf("%w: ttl must be at least %s", limiter.ErrInvalidStoreOperation, minimumTTL)
	}
	return nil
}

func validateTokenOperation(capacity, refillPerSec, n float64, ttl time.Duration) error {
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	switch {
	case !finite(capacity) || capacity <= 0 || capacity > float64(maxExactCounter):
		return fmt.Errorf("%w: capacity must be finite and in (0, %d]", limiter.ErrInvalidStoreOperation, maxExactCounter)
	case !finite(refillPerSec) || refillPerSec <= 0:
		return fmt.Errorf("%w: refill rate must be finite and positive", limiter.ErrInvalidStoreOperation)
	case !finite(n) || n < 0 || n > float64(maxExactCounter):
		return fmt.Errorf("%w: requested tokens must be finite and in [0, %d]", limiter.ErrInvalidStoreOperation, maxExactCounter)
	}

	secondsToFull := capacity / refillPerSec
	refillMicros := math.Ceil(secondsToFull * 1e6)
	maxRefillMicros := float64(maxStoreDuration / time.Microsecond)
	if !finite(secondsToFull) || !finite(refillMicros) || refillMicros > maxRefillMicros {
		return fmt.Errorf("%w: full-refill duration is too large", limiter.ErrInvalidStoreOperation)
	}
	if refillMicros < 1 {
		refillMicros = 1
	}
	minimumTTL := time.Duration(int64(refillMicros)) * time.Microsecond
	if ttl < minimumTTL {
		return fmt.Errorf("%w: ttl must cover a full refill (%s)", limiter.ErrInvalidStoreOperation, minimumTTL)
	}
	return nil
}
