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
	// when needed.
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

// GenaiStore creates explicit cached contents with the Gen AI client.
type GenaiStore struct {
	caches cachesAPI
	ttl    time.Duration
	now    func() time.Time

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
// or the one there is would expire during the next turn.
func (s *GenaiStore) Ensure(ctx context.Context, modelName string, prefix Prefix) (string, error) {
	key := cacheKey(modelName, prefix)

	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, live := s.entries[key]; live && s.now().Add(refreshMargin).Before(entry.expiresAt) {
		return entry.name, nil
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
	if previous, existed := s.entries[key]; existed {
		// The previous entry is not expired yet; drop it so the new one is
		// the only cache billing for this prefix.
		_, _ = s.caches.Delete(ctx, previous.name, nil)
	}
	expiresAt := s.now().Add(s.ttl)
	if !cached.ExpireTime.IsZero() {
		expiresAt = cached.ExpireTime
	}
	s.entries[key] = cacheEntry{name: cached.Name, expiresAt: expiresAt}
	s.pruneExpired(ctx)
	return cached.Name, nil
}

// Close deletes every cache this store created that has not expired yet.
// Vertex deletes an expired cache itself and answers 404 for it, which is
// taken as deleted too.
func (s *GenaiStore) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var failures []error
	for key, entry := range s.entries {
		delete(s.entries, key)
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
	for key, entry := range s.entries {
		if now.Before(entry.expiresAt) {
			continue
		}
		_, _ = s.caches.Delete(ctx, entry.name, nil)
		delete(s.entries, key)
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
