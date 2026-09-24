// Package vectorstore is where the vectors live, behind one interface over
// two very different places.
//
// pgvector keeps them in the shared PostgreSQL, close to the chunk text, and
// answers a filtered search in one statement. S3 Vectors keeps them in Amazon
// S3, for a fraction of the cost per stored vector, and is where a large or
// cold corpus belongs. A corpus names which it was written into and nothing
// above this package has to ask again.
//
// A store deals in identifiers and distances only. The chunk text and its
// provenance stay in PostgreSQL whichever backend holds the vectors, so there
// is one answer to what a chunk says and citations do not depend on which
// index happened to find it.
package vectorstore

import (
	"context"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Search is one nearest-neighbour query, already scoped to a corpus.
type Search struct {
	Vector []float32
	TopK   int
	Filter domain.Filter
	// Assigned by the application from the verified organization role.
	IncludeAdminOnly bool
}

// Hit is one chunk the index matched. Score is the distance turned into a
// number where larger is better, by the corpus's own metric.
type Hit struct {
	ChunkID  string
	Distance float64
	Score    float64
}

// Store is the boundary the retrieval and ingest paths depend on.
type Store interface {
	// Upsert writes vectors for chunks that may already be there. Re-ingesting
	// a document must not leave two vectors for one chunk.
	Upsert(ctx context.Context, corpus domain.Corpus, vectors []domain.Vector) error
	// Search returns the nearest chunks, already scoped to the corpus and its
	// organization.
	Search(ctx context.Context, corpus domain.Corpus, search Search) ([]Hit, error)
	// Delete removes named chunks. The caller knows which chunks a document
	// produced, because PostgreSQL holds them, so neither backend has to
	// support deleting by query.
	Delete(ctx context.Context, corpus domain.Corpus, chunkIDs []string) error
	// DropCorpus removes everything a corpus holds.
	DropCorpus(ctx context.Context, corpus domain.Corpus) error
	// Backend says which of the two this is, for logs and for the router.
	Backend() domain.Backend
}

// Router sends each corpus to the store it was written into.
type Router struct {
	stores map[domain.Backend]Store
}

func NewRouter(stores ...Store) *Router {
	router := &Router{stores: make(map[domain.Backend]Store, len(stores))}
	for _, store := range stores {
		if store != nil {
			router.stores[store.Backend()] = store
		}
	}
	return router
}

// For returns the store holding a corpus's vectors.
//
// A corpus whose backend this deployment was not configured for is an error
// with a name in it, not a silent empty result: an empty answer from a corpus
// full of documents is the hardest kind of failure to notice.
func (router *Router) For(corpus domain.Corpus) (Store, error) {
	store, configured := router.stores[corpus.Backend]
	if !configured {
		return nil, fmt.Errorf("corpus %s is on %q, which this deployment is not configured for: %w",
			corpus.ID, corpus.Backend, domain.ErrInvalid)
	}
	return store, nil
}

// Backends lists what this deployment can serve.
func (router *Router) Backends() []domain.Backend {
	backends := make([]domain.Backend, 0, len(router.stores))
	for _, candidate := range []domain.Backend{domain.BackendPgvector, domain.BackendS3Vectors, domain.BackendBedrockKB} {
		if _, configured := router.stores[candidate]; configured {
			backends = append(backends, candidate)
		}
	}
	return backends
}

// validate checks a batch against the corpus before either backend sees it.
// Both would reject a mis-sized vector eventually; doing it here means the
// same error, with the same wording, whichever store is behind the corpus.
func validate(corpus domain.Corpus, vectors []domain.Vector) error {
	for _, vector := range vectors {
		if err := vector.Validate(corpus.Spec); err != nil {
			return err
		}
	}
	return nil
}
