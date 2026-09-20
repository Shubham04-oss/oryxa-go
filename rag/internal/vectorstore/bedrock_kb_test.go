package vectorstore

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// TestBedrockKnowledgeBaseNeverActuallyServes: every method is a programmer
// error if it is ever reached — ingest, retrieval and corpus or document
// removal all recognise domain.BackendBedrockKB before reaching a
// vectorstore.Store at all — so each one must fail loudly rather than
// silently doing nothing.
func TestBedrockKnowledgeBaseNeverActuallyServes(t *testing.T) {
	t.Parallel()
	store := BedrockKnowledgeBase{}
	if store.Backend() != domain.BackendBedrockKB {
		t.Fatalf("Backend() = %q", store.Backend())
	}
	corpus := domain.Corpus{ID: "cor_1", OrganizationID: "org_1", Backend: domain.BackendBedrockKB}
	if err := store.Upsert(context.Background(), corpus, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Upsert error = %v, want ErrInvalid", err)
	}
	if _, err := store.Search(context.Background(), corpus, Search{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Search error = %v, want ErrInvalid", err)
	}
	if err := store.Delete(context.Background(), corpus, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Delete error = %v, want ErrInvalid", err)
	}
	if err := store.DropCorpus(context.Background(), corpus); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DropCorpus error = %v, want ErrInvalid", err)
	}
}

// TestRouterServesBedrockKBWhenRegistered: the same "is this deployment
// configured for X" question Router.For already answers for pgvector and S3
// Vectors, asked the same way for bedrock-kb, which is what lets
// Service.CreateCorpus use one check for every backend.
func TestRouterServesBedrockKBWhenRegistered(t *testing.T) {
	t.Parallel()
	router := NewRouter(BedrockKnowledgeBase{})
	store, err := router.For(domain.Corpus{Backend: domain.BackendBedrockKB})
	if err != nil || store.Backend() != domain.BackendBedrockKB {
		t.Fatalf("For = (%v, %v)", store, err)
	}
	backends := router.Backends()
	if len(backends) != 1 || backends[0] != domain.BackendBedrockKB {
		t.Fatalf("Backends = %v, want exactly [bedrock-kb]", backends)
	}
}

// TestRouterOmitsBedrockKBWhenNotConfigured: naming it in the candidate list
// Backends checks must not report it available when nothing registered it —
// the same care the pgvector and S3 Vectors entries already get.
func TestRouterOmitsBedrockKBWhenNotConfigured(t *testing.T) {
	t.Parallel()
	router := NewRouter()
	if _, err := router.For(domain.Corpus{Backend: domain.BackendBedrockKB}); err == nil {
		t.Fatal("a bedrock-kb corpus was routed with nothing configured for it")
	}
	if backends := router.Backends(); len(backends) != 0 {
		t.Fatalf("Backends = %v, want none", backends)
	}
}
