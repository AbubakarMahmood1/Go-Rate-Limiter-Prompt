package algorithms

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
)

// SlidingWindowCounter approximates a true sliding window by weighting the
// previous fixed window by how much of it still overlaps the sliding one:
//
//	weighted = current + previous * (1 - elapsed/window)
//
// It smooths the boundary bursts fixed windows allow while storing only two
// counters per key.
type SlidingWindowCounter struct {
	store  limiter.Store
	limit  int
	window time.Duration
	ttl    time.Duration
}

// NewSlidingWindowCounter creates a sliding-window limiter allowing
// config.Limit requests per config.Window.
func NewSlidingWindowCounter(store limiter.Store, config limiter.Config) *SlidingWindowCounter {
	return &SlidingWindowCounter{
		store:  store,
		limit:  config.Limit,
		window: config.Window,
		ttl:    windowRetention(config.Window),
	}
}

// Allow implements limiter.RateLimiter.
func (s *SlidingWindowCounter) Allow(ctx context.Context, key string) (*limiter.Result, error) {
	return s.AllowN(ctx, key, 1)
}

// AllowN implements limiter.RateLimiter.
func (s *SlidingWindowCounter) AllowN(ctx context.Context, key string, n int) (*limiter.Result, error) {
	if n <= 0 {
		return nil, limiter.ErrInvalidCount
	}
	if n > s.limit {
		return nil, limiter.ErrExceedsLimit
	}

	w, err := s.store.IncrWindow(ctx, "sw:"+key, s.window, int64(n), int64(s.limit), true, s.ttl)
	if err != nil {
		return nil, fmt.Errorf("sliding window: %w", err)
	}
	return s.result(w, n), nil
}

// Peek implements limiter.RateLimiter.
func (s *SlidingWindowCounter) Peek(ctx context.Context, key string) (*limiter.Result, error) {
	w, err := s.store.IncrWindow(ctx, "sw:"+key, s.window, 0, int64(s.limit), true, s.ttl)
	if err != nil {
		return nil, fmt.Errorf("sliding window: %w", err)
	}

	r := s.result(w, 1)
	r.Allowed = r.Remaining >= 1
	if !r.Allowed {
		r.RetryAfter = s.retryAfter(w, 1)
	}
	return r, nil
}

// Reset implements limiter.RateLimiter.
func (s *SlidingWindowCounter) Reset(ctx context.Context, key string) error {
	return s.store.Delete(ctx, "sw:"+key)
}

func (s *SlidingWindowCounter) result(w *limiter.WindowResult, n int) *limiter.Result {
	weight := 1 - float64(w.Now.Sub(w.WindowStart))/float64(s.window)
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}
	weighted := float64(w.Current) + float64(w.Previous)*weight

	// Floor the exact remaining budget used by the store's admission formula.
	// Floating-point error may under-report by one permit, but it must never
	// advertise a permit that the next atomic decision would deny.
	remaining := int(math.Floor(float64(s.limit) - weighted))
	if remaining < 0 {
		remaining = 0
	}

	r := &limiter.Result{
		Allowed:   w.Allowed,
		Limit:     s.limit,
		Remaining: remaining,
		ResetAt:   s.fullResetAt(w),
	}
	if !w.Allowed {
		r.RetryAfter = s.retryAfter(w, n)
	}
	return r
}

// retryAfter returns the earliest microsecond at which the same request would
// succeed if no other requests arrive. A lower-bound search mirrors the exact
// floating-point expression used by both stores, avoiding an optimistic
// answer at a rounding boundary. There are two phases: the previous counter
// decays until the current boundary, then the current counter becomes the
// previous counter and decays through the next window.
func (s *SlidingWindowCounter) retryAfter(w *limiter.WindowResult, n int) time.Duration {
	windowUS := s.window.Microseconds()
	if windowUS <= 0 || windowUS > int64(maxDuration/time.Microsecond)/2 {
		return maxDuration
	}

	elapsedUS := w.Now.Sub(w.WindowStart).Microseconds()
	if elapsedUS < 0 {
		elapsedUS = 0
	}
	if elapsedUS > windowUS {
		elapsedUS = windowUS
	}
	if s.slidingAllowsAt(w, n, elapsedUS, windowUS) {
		return 0
	}

	low, high := elapsedUS+1, 2*windowUS
	for low < high {
		mid := low + (high-low)/2
		if s.slidingAllowsAt(w, n, mid, windowUS) {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return time.Duration(low-elapsedUS) * time.Microsecond
}

func (s *SlidingWindowCounter) slidingAllowsAt(w *limiter.WindowResult, n int, offsetUS, windowUS int64) bool {
	weighted := 0.0
	switch {
	case offsetUS < windowUS:
		weighted = float64(w.Current)
		if w.Previous > 0 {
			weighted += float64(w.Previous) * (1 - float64(offsetUS)/float64(windowUS))
		}
	case offsetUS < 2*windowUS:
		weighted = float64(w.Current) * (1 - float64(offsetUS-windowUS)/float64(windowUS))
	}
	return weighted+float64(n) <= float64(s.limit)
}

func (s *SlidingWindowCounter) fullResetAt(w *limiter.WindowResult) time.Time {
	windowEnd := w.WindowStart.Add(s.window)
	switch {
	case w.Current > 0:
		// Current becomes previous at windowEnd and reaches zero one window
		// later if no more permits are consumed.
		return windowEnd.Add(s.window)
	case w.Previous > 0:
		return windowEnd
	default:
		return w.Now
	}
}
