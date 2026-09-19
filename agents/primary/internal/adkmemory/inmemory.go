package adkmemory

import (
	"context"
	"slices"
	"sync"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// InMemory returns a [Backend] that keeps memories in the process, for
// development and tests. What it holds goes with the process.
//
// Each scope gets an ADK in-memory service of its own, so the ranking is
// ADK's and the isolation is this package's: one scope's service is never
// the one another scope reads.
func InMemory() Backend {
	return &inMemoryBackend{scopes: make(map[Scope]*inMemoryScope)}
}

// inMemoryScope is one scope's memories, with the apps they were written
// under, since ADK addresses its own store by app name as well as user.
type inMemoryScope struct {
	service memory.Service
	apps    []string
}

type inMemoryBackend struct {
	mutex  sync.Mutex
	scopes map[Scope]*inMemoryScope
}

var _ Backend = (*inMemoryBackend)(nil)

// Write implements [Backend].
func (b *inMemoryBackend) Write(ctx context.Context, scope Scope, conversation session.Session) error {
	b.mutex.Lock()
	held, ok := b.scopes[scope]
	if !ok {
		held = &inMemoryScope{service: memory.InMemoryService()}
		b.scopes[scope] = held
	}
	if app := conversation.AppName(); !slices.Contains(held.apps, app) {
		held.apps = append(held.apps, app)
	}
	b.mutex.Unlock()

	return held.service.AddSessionToMemory(ctx, conversation)
}

// Search implements [Backend].
func (b *inMemoryBackend) Search(ctx context.Context, scope Scope, query string) ([]memory.Entry, error) {
	b.mutex.Lock()
	held, ok := b.scopes[scope]
	var apps []string
	if ok {
		apps = slices.Clone(held.apps)
	}
	b.mutex.Unlock()
	if !ok {
		return nil, nil
	}

	var entries []memory.Entry
	for _, app := range apps {
		found, err := held.service.SearchMemory(ctx, &memory.SearchRequest{
			AppName: app, UserID: scope.UserID, Query: query,
		})
		if err != nil {
			return nil, err
		}
		if found != nil {
			entries = append(entries, found.Memories...)
		}
	}
	return entries, nil
}

// Forget implements [Backend].
func (b *inMemoryBackend) Forget(_ context.Context, organizationID, userID string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	for scope := range b.scopes {
		if scope.OrganizationID != organizationID {
			continue
		}
		if userID != "" && scope.UserID != userID {
			continue
		}
		delete(b.scopes, scope)
	}
	return nil
}
