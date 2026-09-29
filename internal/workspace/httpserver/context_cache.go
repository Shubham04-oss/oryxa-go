package httpserver

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

const contextCacheLimit = 128

type contextRepository interface {
	VersionNumber(context.Context, string) (int64, error)
	Latest(context.Context, string) (store.Version, error)
}

type cachedContext struct {
	version  int64
	document spec.Document
	usedAt   time.Time
}

// This cache contains configuration facts only. Authorization and the current
// organization roster must still be checked for every context response.
type contextCache struct {
	mu      sync.Mutex
	entries map[string]cachedContext
}

func (cache *contextCache) Get(ctx context.Context, repository contextRepository, organizationID string) (spec.Document, error) {
	version, err := repository.VersionNumber(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		cache.mu.Lock()
		delete(cache.entries, organizationID)
		cache.mu.Unlock()
		return spec.Document{}, nil
	}
	if err != nil {
		return spec.Document{}, err
	}
	cache.mu.Lock()
	cached, exists := cache.entries[organizationID]
	if exists && cached.version == version {
		cached.usedAt = time.Now()
		cache.entries[organizationID] = cached
		document := cloneContext(cached.document)
		cache.mu.Unlock()
		return document, nil
	}
	cache.mu.Unlock()
	stored, err := repository.Latest(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		return spec.Document{}, nil
	}
	if err != nil {
		return spec.Document{}, err
	}
	document, err := spec.ParseStored(stored.Document)
	if err != nil {
		return spec.Document{}, err
	}
	document = cloneContext(document)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]cachedContext{}
	}
	if _, exists := cache.entries[organizationID]; !exists && len(cache.entries) >= contextCacheLimit {
		oldestID := ""
		var oldestTime time.Time
		for id, entry := range cache.entries {
			if oldestID == "" || entry.usedAt.Before(oldestTime) {
				oldestID, oldestTime = id, entry.usedAt
			}
		}
		delete(cache.entries, oldestID)
	}
	cache.entries[organizationID] = cachedContext{version: stored.Number, document: document, usedAt: time.Now()}
	return cloneContext(document), nil
}

func cloneContext(document spec.Document) spec.Document {
	result := spec.Document{Ownership: spec.Ownership{Items: slices.Clone(document.Ownership.Items)}}
	for i := range result.Ownership.Items {
		result.Ownership.Items[i].Tags = slices.Clone(result.Ownership.Items[i].Tags)
	}
	if document.OrgChart != nil {
		chart := *document.OrgChart
		chart.Relationships = slices.Clone(chart.Relationships)
		chart.Authority = slices.Clone(chart.Authority)
		chart.Executives = slices.Clone(chart.Executives)
		result.OrgChart = &chart
	}
	return result
}
