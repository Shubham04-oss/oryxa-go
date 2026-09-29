// Package policy reads the small operational projection of an applied version.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/projection"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
	"sync"
)

type Repository interface {
	Policy(context.Context, string) (int64, []byte, error)
}
type Settings = projection.Settings

// Provider is the shared operational policy boundary used by services.
type Provider interface {
	Get(context.Context, string) (Settings, error)
}

// Noop supplies defaults for hosts without a workspace schema.
type Noop struct{}

func (Noop) Get(context.Context, string) (Settings, error) { return Settings{}, nil }

type cached struct {
	version  int64
	settings Settings
}
type Loader struct {
	repository Repository
	mu         sync.Mutex
	cache      map[string]cached
}

func New(repository Repository) *Loader {
	return &Loader{repository: repository, cache: make(map[string]cached)}
}
func (loader *Loader) Get(ctx context.Context, organizationID string) (Settings, error) {
	if loader == nil || loader.repository == nil {
		return Settings{}, nil
	}
	version, data, err := loader.repository.Policy(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	loader.mu.Lock()
	defer loader.mu.Unlock()
	if entry, ok := loader.cache[organizationID]; ok && entry.version == version {
		return clone(entry.settings), nil
	}
	var settings Settings
	// Unknown projection fields belong to newer releases and remain harmless
	// when the running binary has been rolled back.
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("applied workspace policy is invalid: %w", err)
	}
	if len(loader.cache) >= 1024 {
		clear(loader.cache)
	}
	loader.cache[organizationID] = cached{version: version, settings: settings}
	return clone(settings), nil
}
func clone(settings Settings) Settings {
	if settings.Offer != nil {
		items := append([]string{}, (*settings.Offer)...)
		settings.Offer = &items
	}
	if settings.EmployeeConnections != nil {
		value := *settings.EmployeeConnections
		settings.EmployeeConnections = &value
	}
	if settings.AutoSubscribe != nil {
		value := *settings.AutoSubscribe
		settings.AutoSubscribe = &value
	}
	return settings
}
