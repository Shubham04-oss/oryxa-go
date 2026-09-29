package hooks

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// resourceType is what an endpoint's sealed secrets are bound to, with the
// endpoint's id.
const resourceType = "hook_endpoint"

func binding(endpointID string) secrets.Binding {
	return secrets.Binding{ResourceType: resourceType, ResourceID: endpointID}
}

// generatedSecretBytes is the size of a secret hooks makes: 256 bits, which
// encode to 43 characters, within every app's limit (Graph's clientState
// takes 128, a Google channel's token 256).
const generatedSecretBytes = 32

// generateSecret makes a connection endpoint's secret.
func (s *Service) generateSecret() (string, error) {
	value := make([]byte, generatedSecretBytes)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", fmt.Errorf("generate an endpoint secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// Bounds on a secret an admin enters, such as their own Slack app's signing
// secret. Shorter keys are guessable; longer ones are a mistake.
const (
	minEnteredSecret = 16
	maxEnteredSecret = 1024
)

// checkEnteredSecret checks a secret an admin typed, once the surrounding
// space a paste brings along is trimmed. The message never quotes it.
func checkEnteredSecret(secret secrets.Secret) (string, error) {
	value := strings.TrimSpace(secret.Reveal())
	if len(value) < minEnteredSecret || len(value) > maxEnteredSecret {
		return "", fmt.Errorf("a webhook secret is %d to %d characters: %w", minEnteredSecret, maxEnteredSecret, domain.ErrInvalid)
	}
	for i := 0; i < len(value); i++ {
		if value[i] <= ' ' || value[i] > '~' {
			return "", fmt.Errorf("a webhook secret is printable characters without spaces: %w", domain.ErrInvalid)
		}
	}
	return value, nil
}

func (s *Service) seal(ctx context.Context, endpointID, secret string) (secrets.SealedData, error) {
	sealed, err := s.sealer.Seal(ctx, binding(endpointID), []byte(secret))
	if err != nil {
		return secrets.SealedData{}, fmt.Errorf("%w: seal an endpoint secret: %v", errUnavailable, err)
	}
	return sealed, nil
}

func (s *Service) open(ctx context.Context, endpointID string, sealed secrets.SealedData) ([]byte, error) {
	plaintext, err := s.sealer.Open(ctx, binding(endpointID), sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: open an endpoint secret: %v", errUnavailable, err)
	}
	return plaintext, nil
}

// keys are the secrets a delivery to endpoint may be signed with: the
// endpoint's own, and during a rotation's overlap its previous one too; or,
// for a deployment endpoint, the ones configured for its toolkit. It answers
// none when the endpoint has no secret yet.
func (s *Service) keys(ctx context.Context, endpoint Endpoint, now time.Time) ([][]byte, error) {
	sealed := endpoint.Secret
	if sealed.Current == nil {
		if endpoint.Scope == integration.HookScopeDeployment && !endpoint.capturesSecret() {
			return s.deploymentSecrets[endpoint.Toolkit], nil
		}
		return nil, nil
	}
	overlapping := sealed.Previous != nil && sealed.PreviousUntil != nil && now.Before(*sealed.PreviousUntil)
	opened, ok := s.opened.get(endpoint.ID, sealed.Version, now)
	if !ok {
		current, err := s.open(ctx, endpoint.ID, *sealed.Current)
		if err != nil {
			return nil, err
		}
		opened = openedSecrets{version: sealed.Version, current: current}
		if overlapping {
			if opened.previous, err = s.open(ctx, endpoint.ID, *sealed.Previous); err != nil {
				return nil, err
			}
		}
		s.opened.put(endpoint.ID, opened, now)
	}
	keys := [][]byte{opened.current}
	if overlapping && opened.previous != nil {
		keys = append(keys, opened.previous)
	}
	return keys, nil
}

// How long, and for how many endpoints, opened secrets are kept. Opening one
// is a KMS call, and without this every delivery to an endpoint would make
// one, including a flood of forged ones. A rotation changes the version, so
// a rotated secret is opened afresh.
const (
	openedSecretLifetime = 5 * time.Minute
	maxOpenedSecrets     = 10_000
)

type openedSecrets struct {
	version  int
	current  []byte
	previous []byte
	until    time.Time
}

// openedCache keeps endpoints' opened secrets for a few minutes, by endpoint
// and secret version.
type openedCache struct {
	mu      sync.Mutex
	entries map[string]openedSecrets
}

func (cache *openedCache) get(endpointID string, version int, now time.Time) (openedSecrets, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[endpointID]
	if !ok || entry.version != version || !now.Before(entry.until) {
		return openedSecrets{}, false
	}
	return entry, true
}

func (cache *openedCache) put(endpointID string, entry openedSecrets, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]openedSecrets{}
	}
	if len(cache.entries) >= maxOpenedSecrets {
		for id, old := range cache.entries {
			if !now.Before(old.until) {
				delete(cache.entries, id)
			}
		}
		if len(cache.entries) >= maxOpenedSecrets {
			return
		}
	}
	entry.until = now.Add(openedSecretLifetime)
	cache.entries[endpointID] = entry
}

// forget drops an endpoint's opened secrets, once it is deleted. They are
// not wiped: a delivery being checked on another goroutine may still hold
// them, and wiping a key under it would refuse a genuine delivery.
func (cache *openedCache) forget(endpointID string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, endpointID)
}
