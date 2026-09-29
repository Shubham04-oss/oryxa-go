// Package modelcache gives Vertex prompt caching a control surface: a model
// wrapper that serves a request's stable prefix — the system instruction and
// tool declarations — from an explicit cached content when the prefix is big
// enough to be worth it, and otherwise leaves the request alone so Vertex's
// implicit caching still applies. Model usage reports cached tokens either
// way, so the two layers do not fight.
package modelcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// Prefix is the stable part of a request an explicit cache can hold.
type Prefix struct {
	Instruction *genai.Content
	Tools       []*genai.Tool
}

// Store keeps explicit cached contents, keyed by model and prefix.
type Store interface {
	// Ensure returns the name of a live cache for the prefix, creating one
	// when needed. Many runs call it at once, and a run that needs one
	// prefix must not wait for a run that needs another.
	Ensure(ctx context.Context, modelName string, prefix Prefix) (string, error)
}

// Config controls the caching wrapper.
type Config struct {
	// TTL is how long a created cache lives.
	TTL time.Duration
	// MinTokens is the smallest prefix worth an explicit cache. Vertex rejects
	// small cached contents, so below this the wrapper lets implicit caching
	// do what it can.
	MinTokens int
	// Logger receives cache decisions; slog.Default() when nil.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = time.Hour
	}
	if c.MinTokens <= 0 {
		c.MinTokens = 1024
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Cached wraps a model so requests with a large stable prefix use an explicit
// Vertex cache. A nil store turns the wrapper off, returning the model.
func Cached(inner model.LLM, store Store, config Config) model.LLM {
	if store == nil {
		return inner
	}
	return &cached{LLM: inner, store: store, config: config.withDefaults()}
}

type cached struct {
	model.LLM
	store  Store
	config Config
}

func (c *cached) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	c.apply(ctx, req)
	return c.LLM.GenerateContent(ctx, req, stream)
}

// apply moves the request's stable prefix into an explicit cache. A failure
// degrades to the uncached request rather than failing the turn.
func (c *cached) apply(ctx context.Context, req *model.LLMRequest) {
	if req.Config == nil {
		return
	}
	prefix := Prefix{Instruction: req.Config.SystemInstruction, Tools: req.Config.Tools}
	tokens := EstimateTokens(prefix)
	if tokens < c.config.MinTokens {
		return
	}
	name, err := c.store.Ensure(ctx, c.LLM.Name(), prefix)
	if err != nil {
		c.config.Logger.WarnContext(ctx, "explicit prompt cache unavailable; sending the request uncached", "error", err)
		return
	}
	req.Config.CachedContent = name
	// The cached content already carries the prefix; sending it twice makes
	// Vertex reject the request.
	req.Config.SystemInstruction = nil
	req.Config.Tools = nil
	c.config.Logger.DebugContext(ctx, "serving the prompt prefix from cache", "cache", name, "prefix_tokens", tokens)
}

// EstimateTokens estimates a prefix's size at four characters per token,
// deliberately conservative so a small prefix is never sent to a cache that
// would reject it.
func EstimateTokens(prefix Prefix) int {
	size := 0
	if prefix.Instruction != nil {
		for _, part := range prefix.Instruction.Parts {
			if part != nil {
				size += len(part.Text)
			}
		}
	}
	for _, declared := range prefix.Tools {
		encoded, err := json.Marshal(declared)
		if err == nil {
			size += len(encoded)
		}
	}
	return size / 4
}

// refreshMargin recreates a cache this long before it expires, so no request
// races the expiry while a turn is in flight.
const refreshMargin = 2 * time.Minute

// createTimeout bounds one create of a cached content. A create runs apart
// from the runs waiting for it (see Ensure), so it needs a bound of its own.
const createTimeout = 30 * time.Second

// GenaiStore creates explicit cached contents with the Gen AI client.
type GenaiStore struct {
	caches cachesAPI
	ttl    time.Duration
	now    func() time.Time

	// creates lets the runs that need one prefix share one create of its
	// cache, while a run that needs another prefix goes on beside it.
	creates singleflight.Group
	// closing is held shared by a create and alone by Close. Close waits for
	// a create in flight, so the cache that create makes is deleted with the
	// rest.
	closing sync.RWMutex

	// mu guards entries. It is never held across a call to Vertex.
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cachesAPI interface {
	Create(ctx context.Context, model string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error)
	Delete(ctx context.Context, name string, config *genai.DeleteCachedContentConfig) (*genai.DeleteCachedContentResponse, error)
}

type cacheEntry struct {
	name      string
	expiresAt time.Time
}

// NewGenaiStore caches through the client's Caches API, with entries that
// live for ttl.
func NewGenaiStore(client *genai.Client, ttl time.Duration) *GenaiStore {
	return newStore(client.Caches, ttl, time.Now)
}

func newStore(caches cachesAPI, ttl time.Duration, now func() time.Time) *GenaiStore {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &GenaiStore{caches: caches, ttl: ttl, now: now, entries: map[string]cacheEntry{}}
}

// Ensure returns a live cache for the prefix, creating one when there is none
// or the one there is would expire during the next turn. Runs that need the
// same prefix share one create, and each waits for it until its own context
// ends; a run that needs another prefix does not wait for it at all.
func (s *GenaiStore) Ensure(ctx context.Context, modelName string, prefix Prefix) (string, error) {
	key := cacheKey(modelName, prefix)
	if name, live := s.live(key); live {
		return name, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// The create runs apart from the runs waiting for it. A cache made for a
	// run that has given up is still billed, so it is kept for the next run,
	// and one run being cancelled must not fail the others waiting beside it.
	detached := context.WithoutCancel(ctx)
	results := s.creates.DoChan(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(detached, createTimeout)
		defer cancel()
		return s.create(ctx, key, modelName, prefix)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-results:
		if result.Err != nil {
			return "", result.Err
		}
		return result.Val.(string), nil
	}
}

// live returns the name of the prefix's cache when it is there and would not
// expire during the next turn.
func (s *GenaiStore) live(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || !s.now().Add(refreshMargin).Before(entry.expiresAt) {
		return "", false
	}
	return entry.name, true
}

// create makes a new cache for the prefix, unless one made since the caller
// looked is there.
func (s *GenaiStore) create(ctx context.Context, key, modelName string, prefix Prefix) (string, error) {
	s.closing.RLock()
	defer s.closing.RUnlock()

	if name, live := s.live(key); live {
		return name, nil
	}
	cached, err := s.caches.Create(ctx, modelName, &genai.CreateCachedContentConfig{
		TTL:               s.ttl,
		DisplayName:       "oryxa-agents-" + key[:16],
		SystemInstruction: prefix.Instruction,
		Tools:             prefix.Tools,
	})
	if err != nil {
		return "", fmt.Errorf("create cached content: %w", err)
	}
	if cached == nil || cached.Name == "" {
		return "", errors.New("create cached content returned no name")
	}
	expiresAt := s.now().Add(s.ttl)
	if !cached.ExpireTime.IsZero() {
		expiresAt = cached.ExpireTime
	}
	s.mu.Lock()
	previous, existed := s.entries[key]
	s.entries[key] = cacheEntry{name: cached.Name, expiresAt: expiresAt}
	s.mu.Unlock()
	if existed {
		// The previous entry is not expired yet; drop it so the new one is
		// the only cache billing for this prefix.
		_, _ = s.caches.Delete(ctx, previous.name, nil)
	}
	s.pruneExpired(ctx)
	return cached.Name, nil
}

// Close deletes every cache this store created that has not expired yet,
// after any create in flight has finished. Vertex deletes an expired cache
// itself and answers 404 for it, which is taken as deleted too.
func (s *GenaiStore) Close(ctx context.Context) error {
	s.closing.Lock()
	defer s.closing.Unlock()
	s.mu.Lock()
	entries := s.entries
	s.entries = map[string]cacheEntry{}
	s.mu.Unlock()
	now := s.now()
	var failures []error
	for _, entry := range entries {
		if !now.Before(entry.expiresAt) {
			continue
		}
		if _, err := s.caches.Delete(ctx, entry.name, nil); err != nil && !gone(err) {
			failures = append(failures, fmt.Errorf("delete cached content %q: %w", entry.name, err))
		}
	}
	return errors.Join(failures...)
}

// gone reports whether err is Vertex saying the cached content no longer
// exists, as it does once a cache has expired or been deleted elsewhere.
func gone(err error) bool {
	var apiErr genai.APIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound
}

// pruneExpired best-effort deletes entries that are already gone server-side.
func (s *GenaiStore) pruneExpired(ctx context.Context) {
	now := s.now()
	var expired []string
	s.mu.Lock()
	for key, entry := range s.entries {
		if now.Before(entry.expiresAt) {
			continue
		}
		expired = append(expired, entry.name)
		delete(s.entries, key)
	}
	s.mu.Unlock()
	for _, name := range expired {
		_, _ = s.caches.Delete(ctx, name, nil)
	}
}

// cacheKey hashes the model and every byte of the prefix, so a changed
// instruction or tool declaration never serves a stale cache.
func cacheKey(modelName string, prefix Prefix) string {
	digest := sha256.New()
	io.WriteString(digest, modelName)
	if prefix.Instruction != nil {
		for _, part := range prefix.Instruction.Parts {
			if part != nil {
				io.WriteString(digest, part.Text)
			}
		}
	}
	for _, declared := range prefix.Tools {
		if encoded, err := json.Marshal(declared); err == nil {
			digest.Write(encoded)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}
