package vectorstore

import (
	"context"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// BedrockKnowledgeBase stands in for the bedrock-kb backend in the Router.
//
// A corpus on this backend is never chunked or embedded by rag — Bedrock
// does that itself — so nothing here ever calls Upsert, Search or Delete on
// it: ingest, retrieval and corpus or document removal all recognise
// domain.BackendBedrockKB before reaching a vectorstore.Store at all, and
// talk to the knowledge base directly through the knowledgebase package
// instead (see rag/internal/ingest, rag/internal/retrieval and
// application.Service.dropVectors). This type exists only so the Router has
// something to hand back for "is this deployment configured for bedrock-kb",
// exactly as it does for pgvector and S3 Vectors — the same question, asked
// the same way, whichever backend a corpus names — which is what lets
// Service.CreateCorpus and Service.knowledgeCorpus use one check for all
// three rather than one backend needing a check of its own.
//
// Every method below is a programmer error if it is ever actually called,
// so each fails loudly by name rather than doing nothing or, worse,
// misreporting a corpus other backends really do serve through these calls.
type BedrockKnowledgeBase struct{}

func (BedrockKnowledgeBase) Backend() domain.Backend { return domain.BackendBedrockKB }

func (BedrockKnowledgeBase) Upsert(context.Context, domain.Corpus, []domain.Vector) error {
	return fmt.Errorf("a bedrock-kb corpus is never written through vectorstore.Store: %w", domain.ErrInvalid)
}

func (BedrockKnowledgeBase) Search(context.Context, domain.Corpus, Search) ([]Hit, error) {
	return nil, fmt.Errorf("a bedrock-kb corpus is never searched through vectorstore.Store: %w", domain.ErrInvalid)
}

func (BedrockKnowledgeBase) Delete(context.Context, domain.Corpus, []string) error {
	return fmt.Errorf("a bedrock-kb corpus is never deleted through vectorstore.Store: %w", domain.ErrInvalid)
}

func (BedrockKnowledgeBase) DropCorpus(context.Context, domain.Corpus) error {
	return fmt.Errorf("a bedrock-kb corpus is never dropped through vectorstore.Store: %w", domain.ErrInvalid)
}
