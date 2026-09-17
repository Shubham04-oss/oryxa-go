package embedding

import (
	"context"
	"fmt"
	"sync"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Config is everything needed to serve any spec a corpus might have pinned.
//
// Both providers are configured up front and neither is required. A
// deployment with only AWS credentials serves Bedrock corpora and refuses
// Vertex ones with a clear message, rather than failing at the first
// embedding call inside a background ingest.
type Config struct {
	Bedrock InvokeAPI
	Vertex  VertexConfig
	Options Options
}

// Registry hands out an embedder per embedding spec, and keeps it.
//
// Every corpus names a spec, and many corpora name the same one. Building an
// embedder is not free — the Vertex path resolves application default
// credentials and holds a token source that refreshes itself — so building
// one per request would re-authenticate on every query.
type Registry struct {
	config    Config
	mutex     sync.Mutex
	embedders map[domain.EmbeddingSpec]Embedder
}

func NewRegistry(config Config) *Registry {
	return &Registry{config: config, embedders: map[domain.EmbeddingSpec]Embedder{}}
}

// For returns the embedder that writes into spec's vector space.
func (registry *Registry) For(ctx context.Context, spec domain.EmbeddingSpec) (Embedder, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if embedder, built := registry.embedders[spec]; built {
		return embedder, nil
	}
	embedder, err := registry.build(ctx, spec)
	if err != nil {
		return nil, err
	}
	registry.embedders[spec] = embedder
	return embedder, nil
}

func (registry *Registry) build(ctx context.Context, spec domain.EmbeddingSpec) (Embedder, error) {
	switch spec.Provider {
	case domain.ProviderBedrock:
		if registry.config.Bedrock == nil {
			return nil, fmt.Errorf("this deployment has no Bedrock client, so it cannot serve %s: %w", spec, domain.ErrInvalid)
		}
		return NewBedrock(registry.config.Bedrock, spec, registry.config.Options)
	case domain.ProviderVertex:
		if registry.config.Vertex.Project == "" {
			return nil, fmt.Errorf("this deployment has no Google Cloud project, so it cannot serve %s: %w", spec, domain.ErrInvalid)
		}
		return NewVertex(ctx, spec, registry.config.Vertex, registry.config.Options)
	default:
		return nil, fmt.Errorf("embedding provider %q is not known: %w", spec.Provider, domain.ErrInvalid)
	}
}

// Providers lists which providers this deployment can actually serve, for the
// API to advertise and for a startup log line.
func (registry *Registry) Providers() []domain.Provider {
	var providers []domain.Provider
	if registry.config.Bedrock != nil {
		providers = append(providers, domain.ProviderBedrock)
	}
	if registry.config.Vertex.Project != "" {
		providers = append(providers, domain.ProviderVertex)
	}
	return providers
}
