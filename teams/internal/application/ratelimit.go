package application

import (
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// toolCallBudget is how many ping-bearing tool calls one member may make per
// organization per window. Each accepted call writes an event per member of
// every team it touched, and each of those can become an email, so a member
// hammering the endpoint costs their organization mail and queue depth rather
// than merely a row.
const toolCallBudget = 20

// toolCallWindow is how long the budget lasts. Generous for a person clicking
// around, and small next to what one member could otherwise spend: the chaos
// rig's plain member made 200 calls — a thousand outbox rows — in a second.
const toolCallWindow = time.Minute

// toolRateLimiter holds one token bucket per member per organization. Tokens
// refill continuously and idle buckets are pruned, so the map holds only
// members who have called recently.
type toolRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]float64
	last    map[string]time.Time
	now     func() time.Time
}

func newToolRateLimiter(now func() time.Time) *toolRateLimiter {
	return &toolRateLimiter{buckets: map[string]float64{}, last: map[string]time.Time{}, now: now}
}

// allow spends one token for key, reporting whether it was there to spend.
func (limiter *toolRateLimiter) allow(key string) bool {
	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.buckets) >= 10_000 {
		limiter.pruneLocked(now)
	}
	tokens, seen := limiter.buckets[key]
	if !seen {
		tokens = toolCallBudget
	} else if elapsed := now.Sub(limiter.last[key]); elapsed > 0 {
		tokens = min(tokens+float64(elapsed)/float64(toolCallWindow)*toolCallBudget, toolCallBudget)
	}
	if tokens < 1 {
		limiter.buckets[key], limiter.last[key] = tokens, now
		return false
	}
	limiter.buckets[key], limiter.last[key] = tokens-1, now
	return true
}

// pruneLocked drops buckets that have sat idle past a full window; a key
// unseen for that long starts at a full bucket, so nothing is lost by
// forgetting it. Called with the lock held.
func (limiter *toolRateLimiter) pruneLocked(now time.Time) {
	for key, last := range limiter.last {
		if now.Sub(last) > toolCallWindow {
			delete(limiter.buckets, key)
			delete(limiter.last, key)
		}
	}
}

// allowToolCall answers whether this member may make one more ping-bearing
// call in this organization. It is consulted for list_teams, call_team and
// call_team_members — the three tools that write a MemberPinged event for
// everyone a member's reach touches.
func (s *Service) allowToolCall(organizationID, memberID string) error {
	if !s.toolCalls.allow(organizationID + "/" + memberID) {
		return domain.ErrTooManyRequests
	}
	return nil
}
