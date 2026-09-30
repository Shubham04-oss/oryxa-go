package slackbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

const (
	// postsPerSecond and postBurst are each organization's budget of calls to
	// Slack's posting methods. Slack allows about a message a second to one
	// channel, and every ask makes a few; the budget keeps one busy
	// organization, or a loop, from spending the bot's standing with Slack for
	// everyone else. It counts per replica.
	postsPerSecond = 3
	postBurst      = 15

	// maxBuckets is how many organizations' buckets are held before idle ones
	// are pruned.
	maxBuckets = 10_000

	// tokenLifetime is how long an opened bot token is kept, so a burst of
	// posts opens it once and not once each: opening is a KMS call.
	tokenLifetime = 5 * time.Minute
	maxTokens     = 1_000

	// maxDMs bounds the direct-message conversations remembered.
	maxDMs = 10_000
)

// limiter is a token bucket per organization. Tokens refill continuously, and
// a bucket unseen for long enough is full again and is forgotten.
type limiter struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu     sync.Mutex
	tokens map[string]float64
	last   map[string]time.Time
}

func newLimiter(rate, burst int, now func() time.Time) *limiter {
	return &limiter{
		rate: float64(rate), burst: float64(burst), now: now,
		tokens: map[string]float64{}, last: map[string]time.Time{},
	}
}

// take spends one token of key's bucket. It answers 0 when there was one, and
// otherwise how long until there is.
func (l *limiter) take(key string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.tokens) >= maxBuckets {
		l.pruneLocked(now)
	}
	tokens, seen := l.tokens[key]
	if !seen {
		tokens = l.burst
	} else if elapsed := now.Sub(l.last[key]); elapsed > 0 {
		tokens = min(tokens+elapsed.Seconds()*l.rate, l.burst)
	}
	l.last[key] = now
	if tokens < 1 {
		l.tokens[key] = tokens
		return time.Duration((1 - tokens) / l.rate * float64(time.Second))
	}
	l.tokens[key] = tokens - 1
	return 0
}

// pruneLocked forgets buckets idle for long enough to have refilled.
func (l *limiter) pruneLocked(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for key, last := range l.last {
		if now.Sub(last) > full {
			delete(l.tokens, key)
			delete(l.last, key)
		}
	}
}

// spend takes a token for an organization, or is the rate limit to answer.
func (s *Service) spend(organizationID string) error {
	if wait := s.limiter.take(organizationID); wait > 0 {
		return &RateLimitedError{RetryAfter: wait}
	}
	return nil
}

// tokenCache keeps opened bot tokens for a few minutes, by team, so long as
// the sealed token they came from is still the install's.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]cachedToken
}

type cachedToken struct {
	token       secrets.Secret
	fingerprint string
	until       time.Time
}

func fingerprint(sealed secrets.SealedData) string {
	sum := sha256.Sum256(append(append([]byte{}, sealed.Nonce...), sealed.Ciphertext...))
	return hex.EncodeToString(sum[:8])
}

func (c *tokenCache) get(teamID, print string, now time.Time) (secrets.Secret, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[teamID]
	if !ok || entry.fingerprint != print || !now.Before(entry.until) {
		return "", false
	}
	return entry.token, true
}

func (c *tokenCache) put(teamID, print string, token secrets.Secret, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedToken{}
	}
	if len(c.entries) >= maxTokens {
		for id, entry := range c.entries {
			if !now.Before(entry.until) {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= maxTokens {
			return
		}
	}
	c.entries[teamID] = cachedToken{token: token, fingerprint: print, until: now.Add(tokenLifetime)}
}

func (c *tokenCache) forget(teamID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, teamID)
}

func tokenBinding(teamID string) secrets.Binding {
	return secrets.Binding{ResourceType: SealResource, ResourceID: teamID}
}

// botToken opens an install's bot token. KMS not answering is
// domain.ErrUnavailable: nothing is wrong with the token.
func (s *Service) botToken(ctx context.Context, install Install) (secrets.Secret, error) {
	if !install.Active() || install.Sealed == nil {
		return "", fmt.Errorf("the Slack install has ended: %w", domain.ErrNotConnected)
	}
	now := s.now()
	print := fingerprint(*install.Sealed)
	if token, ok := s.tokens.get(install.TeamID, print, now); ok {
		return token, nil
	}
	plaintext, err := s.sealer.Open(ctx, tokenBinding(install.TeamID), *install.Sealed)
	if err != nil {
		return "", fmt.Errorf("%w: open the Slack bot token: %v", domain.ErrUnavailable, err)
	}
	defer secrets.Wipe(plaintext)
	token := secrets.Secret(plaintext)
	s.tokens.put(install.TeamID, print, token, now)
	return token, nil
}

// dmCache remembers the conversation the bot has with a person, which never
// changes, so a notice is not preceded by a call for it. Losing it costs one
// call.
type dmCache struct {
	mu      sync.Mutex
	entries map[string]string
}

func (c *dmCache) get(teamID, userID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	channel, ok := c.entries[teamID+"/"+userID]
	return channel, ok
}

func (c *dmCache) put(teamID, userID, channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxDMs {
		c.entries = map[string]string{}
	}
	c.entries[teamID+"/"+userID] = channel
}
