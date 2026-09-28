package hooks

import (
	"sync"
	"time"
)

// maxBuckets is how many endpoints' buckets are held before idle ones are
// pruned.
const maxBuckets = 10_000

// limiter holds one token bucket per endpoint, as teams' tool-call limiter
// does per member. Tokens refill continuously, a bucket unseen for a full
// window is pruned (it would be full again anyway), and the map holds only
// endpoints that received something recently. It counts per replica.
type limiter struct {
	budget float64
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]float64
	last    map[string]time.Time
}

func newLimiter(budget int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{
		budget: float64(budget), window: window, now: now,
		buckets: map[string]float64{}, last: map[string]time.Time{},
	}
}

// allow spends one token for key, and says whether it was there to spend.
func (limiter *limiter) allow(key string) bool {
	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.buckets) >= maxBuckets {
		limiter.pruneLocked(now)
	}
	tokens, seen := limiter.buckets[key]
	if !seen {
		tokens = limiter.budget
	} else if elapsed := now.Sub(limiter.last[key]); elapsed > 0 {
		tokens = min(tokens+float64(elapsed)/float64(limiter.window)*limiter.budget, limiter.budget)
	}
	if tokens < 1 {
		limiter.buckets[key], limiter.last[key] = tokens, now
		return false
	}
	limiter.buckets[key], limiter.last[key] = tokens-1, now
	return true
}

// pruneLocked drops buckets idle for longer than a window. Called with the
// lock held.
func (limiter *limiter) pruneLocked(now time.Time) {
	for key, last := range limiter.last {
		if now.Sub(last) > limiter.window {
			delete(limiter.buckets, key)
			delete(limiter.last, key)
		}
	}
}
