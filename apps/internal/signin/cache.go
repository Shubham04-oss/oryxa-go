package signin

import (
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// material is what a request carries to prove a credential: an access
// token, an API token, or a username and password, and the secret fields
// placed beside it. It never holds a refresh token or a client secret, so
// neither is ever cached.
type material struct {
	token    secrets.Secret
	username string
	password secrets.Secret
	fields   map[string]secrets.Secret
	// expires is when the access token expires, or nil when it does not.
	expires *time.Time
}

// tokenCache holds each connection's unsealed material in this process, so
// a call does not open it with KMS every time. An entry is for one version
// of the credential: a refresh or a replacement moves the version on, and
// the old entry is never answered again. It lasts until two minutes before
// the token expires, and at most cacheMaxAge.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	version  int64
	material material
	until    time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[string]cacheEntry{}}
}

// cacheUntil is how long material read at now may be held.
func cacheUntil(held material, now time.Time) time.Time {
	until := now.Add(cacheMaxAge)
	if held.expires != nil {
		if renew := held.expires.Add(-refreshBefore); renew.Before(until) {
			until = renew
		}
	}
	return until
}

func (c *tokenCache) get(connectionID string, version int64, now time.Time) (material, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[connectionID]
	if !ok {
		return material{}, false
	}
	if !now.Before(entry.until) {
		delete(c.entries, connectionID)
		return material{}, false
	}
	if entry.version != version {
		return material{}, false
	}
	return entry.material, true
}

// put holds material for a version until until, unless it is already over,
// or a later version is held.
func (c *tokenCache) put(connectionID string, version int64, held material, until, now time.Time) {
	if !now.Before(until) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[connectionID]; ok && existing.version > version && now.Before(existing.until) {
		return
	}
	c.entries[connectionID] = cacheEntry{version: version, material: held, until: until}
}

func (c *tokenCache) forget(connectionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, connectionID)
}
