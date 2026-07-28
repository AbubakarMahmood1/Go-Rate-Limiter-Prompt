package store

import (
	"context"
	"sync"
	"time"

	"github.com/AbubakarMahmood/go-rate-limiter/pkg/limiter"
)

// janitorInterval controls how often expired entries are evicted.
const janitorInterval = 30 * time.Second

// MemoryStore is an in-process limiter.Store for single-instance deployments
// and tests. Atomicity is provided by a mutex per key, so unrelated keys
// never contend with each other.
type MemoryStore struct {
	mu      sync.RWMutex
	windows map[string]*windowEntry
	buckets map[string]*bucketEntry
	now     func() time.Time

	stopOnce sync.Once
	stop     chan struct{}
}

// windowEntry holds the two windows a decision can depend on. Older windows
// carry no information, so state is O(1) per key regardless of uptime.
type windowEntry struct {
	mu        sync.Mutex
	deleted   bool  // set by the janitor; holders must re-fetch from the map
	curStart  int64 // unix microseconds
	lastNow   int64 // monotonic per-key decision clock, unix microseconds
	cur, prev int64
	expiresAt int64 // unix microseconds
}

type bucketEntry struct {
	mu          sync.Mutex
	deleted     bool
	initialized bool
	tokens      float64
	tsUS        int64 // monotonic per-key refill clock, unix microseconds
	expiresAt   int64 // unix microseconds
}

// NewMemoryStore creates an in-memory store and starts its eviction loop.
// Call Close to stop the loop.
func NewMemoryStore() *MemoryStore {
	return newMemoryStore(time.Now)
}

// newMemoryStore accepts a clock so boundary behaviour can be tested without
// sleeps. Production callers use NewMemoryStore.
func newMemoryStore(now func() time.Time) *MemoryStore {
	ms := &MemoryStore{
		windows: make(map[string]*windowEntry),
		buckets: make(map[string]*bucketEntry),
		now:     now,
		stop:    make(chan struct{}),
	}
	go ms.janitor()
	return ms
}

// IncrWindow implements limiter.Store.
func (ms *MemoryStore) IncrWindow(ctx context.Context, key string, window time.Duration, n, limit int64, weightPrev bool, ttl time.Duration) (*limiter.WindowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateWindowOperation(window, n, limit, weightPrev, ttl); err != nil {
		return nil, err
	}
	// A peek or an intrinsically impossible request never needs to create or
	// mutate state. This mirrors Redis, where the scripts write only after a
	// positive admission.
	readOnly := n == 0 || n > limit
	var e *windowEntry
	if readOnly {
		var ok bool
		e, ok = ms.lookupWindowEntry(key)
		if !ok {
			return ms.emptyWindowResult(window, n, limit), nil
		}
	} else {
		e = ms.windowEntry(key)
	}

	for {
		e.mu.Lock()
		if err := ctx.Err(); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		if e.deleted {
			e.mu.Unlock()
			if readOnly {
				var ok bool
				e, ok = ms.lookupWindowEntry(key)
				if !ok {
					return ms.emptyWindowResult(window, n, limit), nil
				}
			} else {
				e = ms.windowEntry(key)
			}
			continue // evicted between lookup and lock; fetch a fresh entry
		}

		nowUS := ms.now().UnixMicro()
		if nowUS < e.lastNow {
			nowUS = e.lastNow
		}
		w := window.Microseconds()
		start := nowUS - nowUS%w
		curStart, cur, prev := e.curStart, e.cur, e.prev

		switch {
		case curStart == start:
			// still in the same window
		case curStart == start-w:
			prev, cur, curStart = cur, 0, start
		default:
			prev, cur, curStart = 0, 0, start
		}

		weighted := float64(cur)
		if weightPrev && prev > 0 {
			weighted += float64(prev) * (1 - float64(nowUS-start)/float64(w))
		}

		allowed := weighted+float64(n) <= float64(limit)
		if allowed && n > 0 {
			cur += n
			e.curStart, e.cur, e.prev = curStart, cur, prev
			e.lastNow = nowUS
			e.expiresAt = nowUS + durationMicrosCeil(ttl)
		}

		res := &limiter.WindowResult{
			Allowed:     allowed,
			Current:     cur,
			Previous:    prev,
			WindowStart: time.UnixMicro(start),
			Now:         time.UnixMicro(nowUS),
		}
		e.mu.Unlock()
		return res, nil
	}
}

// TakeTokens implements limiter.Store.
func (ms *MemoryStore) TakeTokens(ctx context.Context, key string, capacity, refillPerSec, n float64, ttl time.Duration) (*limiter.TokenResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTokenOperation(capacity, refillPerSec, n, ttl); err != nil {
		return nil, err
	}
	readOnly := n == 0 || n > capacity
	var e *bucketEntry
	if readOnly {
		var ok bool
		e, ok = ms.lookupBucketEntry(key)
		if !ok {
			return ms.emptyTokenResult(capacity, n), nil
		}
	} else {
		e = ms.bucketEntry(key)
	}

	for {
		e.mu.Lock()
		if err := ctx.Err(); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		if e.deleted {
			e.mu.Unlock()
			if readOnly {
				var ok bool
				e, ok = ms.lookupBucketEntry(key)
				if !ok {
					return ms.emptyTokenResult(capacity, n), nil
				}
			} else {
				e = ms.bucketEntry(key)
			}
			continue
		}

		nowUS := ms.now().UnixMicro()
		tokens, tsUS := capacity, nowUS
		if e.initialized {
			tokens, tsUS = e.tokens, e.tsUS
		}
		if nowUS < tsUS {
			nowUS = tsUS
		}

		if elapsedUS := nowUS - tsUS; elapsedUS > 0 {
			tokens += float64(elapsedUS) / 1e6 * refillPerSec
			if tokens > capacity {
				tokens = capacity
			}
		}

		allowed := n <= tokens
		if allowed && n > 0 {
			tokens -= n
			e.initialized = true
			e.tokens = tokens
			e.tsUS = nowUS
			e.expiresAt = nowUS + durationMicrosCeil(ttl)
		}

		result := &limiter.TokenResult{Allowed: allowed, Tokens: tokens, Now: time.UnixMicro(nowUS)}
		e.mu.Unlock()
		return result, nil
	}
}

// Delete implements limiter.Store.
func (ms *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if e, ok := ms.windows[key]; ok {
		e.mu.Lock()
		e.deleted = true
		e.mu.Unlock()
		delete(ms.windows, key)
	}
	if e, ok := ms.buckets[key]; ok {
		e.mu.Lock()
		e.deleted = true
		e.mu.Unlock()
		delete(ms.buckets, key)
	}
	return nil
}

// durationMicrosCeil converts a positive duration to the store clock's
// microsecond resolution without expiring state earlier than requested.
func durationMicrosCeil(d time.Duration) int64 {
	micros := d / time.Microsecond
	if d%time.Microsecond != 0 {
		micros++
	}
	return int64(micros)
}

// Ping implements limiter.Store.
func (ms *MemoryStore) Ping(ctx context.Context) error { return ctx.Err() }

// Close stops the eviction loop.
func (ms *MemoryStore) Close() error {
	ms.stopOnce.Do(func() { close(ms.stop) })
	return nil
}

func (ms *MemoryStore) windowEntry(key string) *windowEntry {
	ms.mu.RLock()
	e, ok := ms.windows[key]
	ms.mu.RUnlock()
	if ok {
		return e
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if e, ok := ms.windows[key]; ok {
		return e
	}
	e = &windowEntry{}
	ms.windows[key] = e
	return e
}

func (ms *MemoryStore) lookupWindowEntry(key string) (*windowEntry, bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	e, ok := ms.windows[key]
	return e, ok
}

func (ms *MemoryStore) emptyWindowResult(window time.Duration, n, limit int64) *limiter.WindowResult {
	nowUS := ms.now().UnixMicro()
	w := window.Microseconds()
	start := nowUS - nowUS%w
	return &limiter.WindowResult{
		Allowed:     n <= limit,
		WindowStart: time.UnixMicro(start),
		Now:         time.UnixMicro(nowUS),
	}
}

func (ms *MemoryStore) bucketEntry(key string) *bucketEntry {
	ms.mu.RLock()
	e, ok := ms.buckets[key]
	ms.mu.RUnlock()
	if ok {
		return e
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if e, ok := ms.buckets[key]; ok {
		return e
	}
	e = &bucketEntry{}
	ms.buckets[key] = e
	return e
}

func (ms *MemoryStore) lookupBucketEntry(key string) (*bucketEntry, bool) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	e, ok := ms.buckets[key]
	return e, ok
}

func (ms *MemoryStore) emptyTokenResult(capacity, n float64) *limiter.TokenResult {
	nowUS := ms.now().UnixMicro()
	return &limiter.TokenResult{
		Allowed: n <= capacity,
		Tokens:  capacity,
		Now:     time.UnixMicro(nowUS),
	}
}

// janitor evicts entries whose TTL has passed. Expiry is equivalent to a
// rolled-over window (counters) or a fully refilled bucket (tokens), so
// eviction never changes observable behaviour.
func (ms *MemoryStore) janitor() {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ms.stop:
			return
		case <-ticker.C:
			ms.sweep(ms.now().UnixMicro())
		}
	}
}

// sweep evicts every entry that expired before cutoff (unix microseconds).
func (ms *MemoryStore) sweep(cutoff int64) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for key, e := range ms.windows {
		e.mu.Lock()
		if e.expiresAt != 0 && e.expiresAt <= cutoff {
			e.deleted = true
			delete(ms.windows, key)
		}
		e.mu.Unlock()
	}
	for key, e := range ms.buckets {
		e.mu.Lock()
		if e.expiresAt != 0 && e.expiresAt <= cutoff {
			e.deleted = true
			delete(ms.buckets, key)
		}
		e.mu.Unlock()
	}
}
