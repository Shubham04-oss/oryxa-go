package application

import (
	"sync"
	"time"
)

// Rate limits on Oryxa's own sign-in, per replica.
const (
	// ConnectBudget is how many connects and key replacements one member may
	// start in one organization per ConnectWindow. Each takes the member away
	// to an app or tries a key against one, so a person never needs more,
	// and a script trying keys gets no further.
	ConnectBudget = 10
	ConnectWindow = time.Minute
	// CallbackBudget is how many OAuth callbacks one address may bring back
	// per CallbackWindow.
	CallbackBudget = 30
	CallbackWindow = time.Minute
)

// maxBuckets is how many keys' buckets are held before idle ones are pruned.
const maxBuckets = 10_000

// RateLimiter holds one token bucket per key, as teams' tool-call limiter
// does per member: tokens refill continuously, and a bucket unseen for a full
// window is pruned, since it would be full again anyway. It counts per
// replica.
type RateLimiter struct {
	budget float64
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]float64
	last    map[string]time.Time
}

// NewRateLimiter answers a limiter of budget per window.
func NewRateLimiter(budget int, window time.Duration, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{
		budget: float64(budget), window: window, now: now,
		buckets: map[string]float64{}, last: map[string]time.Time{},
	}
}

// Allow spends one token for key, and says whether it was there to spend.
func (limiter *RateLimiter) Allow(key string) bool {
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
func (limiter *RateLimiter) pruneLocked(now time.Time) {
	for key, last := range limiter.last {
		if now.Sub(last) > limiter.window {
			delete(limiter.buckets, key)
			delete(limiter.last, key)
		}
	}
}
