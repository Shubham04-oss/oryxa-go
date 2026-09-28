package hooks

import (
	"fmt"
	"testing"
	"time"
)

func TestTheLimiterRefillsContinuouslyPerKey(t *testing.T) {
	clock := &testClock{now: testStart}
	limiter := newLimiter(RatePerSecond, time.Second, clock.Now)
	for i := range RatePerSecond {
		if !limiter.allow("aph_a") {
			t.Fatalf("call %d inside the budget refused", i+1)
		}
	}
	if limiter.allow("aph_a") {
		t.Fatal("a call past the budget allowed")
	}
	if !limiter.allow("aph_b") {
		t.Fatal("another key shares the first one's bucket")
	}
	// One token comes back every 1/50th of a second.
	clock.Advance(time.Second / RatePerSecond)
	if !limiter.allow("aph_a") || limiter.allow("aph_a") {
		t.Fatal("a fiftieth of a second should refill exactly one token")
	}
	// A bucket never holds more than its budget, however long it sits.
	clock.Advance(time.Hour)
	allowed := 0
	for range 2 * RatePerSecond {
		if limiter.allow("aph_a") {
			allowed++
		}
	}
	if allowed != RatePerSecond {
		t.Fatalf("after an hour idle, %d calls allowed, want the budget of %d", allowed, RatePerSecond)
	}
}

func TestTheLimiterForgetsIdleBuckets(t *testing.T) {
	clock := &testClock{now: testStart}
	limiter := newLimiter(RatePerSecond, time.Second, clock.Now)
	for i := range maxBuckets {
		limiter.allow(fmt.Sprint("aph_", i))
	}
	clock.Advance(2 * time.Second)
	limiter.allow("aph_new")
	if len(limiter.buckets) != 1 || len(limiter.last) != 1 {
		t.Fatalf("%d buckets after pruning idle ones, want only the new one", len(limiter.buckets))
	}
}
