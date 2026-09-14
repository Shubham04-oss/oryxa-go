package blob

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

type Object struct {
	Key         string
	ContentType string
	SHA256      string
	Body        []byte
}

type Store interface {
	Put(context.Context, Object) error
	Get(context.Context, string) (Object, error)
	Delete(context.Context, string) error
	DeletePrefix(context.Context, string) error
	PresignGet(context.Context, string, time.Duration) (string, error)
}

// Memory is the in-process stand-in used by unit tests. It is not a
// production store.
type Memory struct {
	mu      sync.Mutex
	objects map[string]Object
}

func NewMemory() *Memory {
	return &Memory{objects: map[string]Object{}}
}

func (store *Memory) Put(_ context.Context, object Object) error {
	if object.Key == "" || len(object.Body) == 0 {
		return fmt.Errorf("object key and body are required: %w", domain.ErrInvalid)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	copied := make([]byte, len(object.Body))
	copy(copied, object.Body)
	object.Body = copied
	store.objects[object.Key] = object
	return nil
}

func (store *Memory) Get(_ context.Context, key string) (Object, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	object, ok := store.objects[key]
	if !ok {
		return Object{}, domain.ErrNotFound
	}
	copied := make([]byte, len(object.Body))
	copy(copied, object.Body)
	object.Body = copied
	return object, nil
}

func (store *Memory) Delete(_ context.Context, key string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.objects[key]; !ok {
		return domain.ErrNotFound
	}
	delete(store.objects, key)
	return nil
}

func (store *Memory) DeletePrefix(_ context.Context, prefix string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key := range store.objects {
		if len(prefix) == 0 || (len(key) >= len(prefix) && key[:len(prefix)] == prefix) {
			delete(store.objects, key)
		}
	}
	return nil
}

func (store *Memory) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.objects[key]; !ok {
		return "", domain.ErrNotFound
	}
	return "memory://" + key, nil
}
