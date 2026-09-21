package eval

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// identifierMessage is one kind of templated mail: many messages alike in
// every word but the number only each one carries, which is how members'
// invoices, orders and support requests look.
type identifierMessage struct {
	kind     string
	id       func(*rand.Rand) string
	body     string
	question string
}

var identifierMessages = []identifierMessage{{
	kind: "invoice",
	id:   func(random *rand.Rand) string { return fmt.Sprintf("INV-2026-%05d", random.IntN(100000)) },
	body: "Subject: Invoice %[1]s\n\nHi,\n\nPlease find attached invoice %[1]s for the services delivered this month. " +
		"The amount is due within 30 days of this email. Reply to this message if anything on it looks wrong.\n\nThanks,\nAccounts team",
	question: "Has invoice %s been paid?",
}, {
	kind: "order",
	id: func(random *rand.Rand) string {
		return fmt.Sprintf("%c%c%c-%06d", 'A'+random.IntN(26), 'A'+random.IntN(26), 'A'+random.IntN(26), random.IntN(1000000))
	},
	body: "Subject: Your order %[1]s has shipped\n\nGood news: order %[1]s left our warehouse today and should arrive " +
		"within five working days. You can follow it from your account page.\n\nThe shop team",
	question: "Where is my order %s?",
}, {
	kind: "ticket",
	id:   func(random *rand.Rand) string { return fmt.Sprintf("CASE-%05d", random.IntN(100000)) },
	body: "Subject: Support request %[1]s updated\n\nHello,\n\nWe have added a reply to your support request %[1]s. " +
		"Please sign in to read it and tell us whether it solves the problem.\n\nSupport",
	question: "What happened with support request %s?",
}}

// TestIdentifierRecall measures what keyword search adds that meaning alone
// cannot: finding the one message among near-identical ones by the number
// only it carries. It runs only against a knowledge base, configured as
// TestRetrievalQuality's is, and reports rather than asserts.
//
//	RAG_EVAL_DATABASE_URL=… RAG_EVAL_BACKEND=bedrock-kb RAG_EVAL_KNOWLEDGE_BASE_ID=… \
//	  RAG_EVAL_KNOWLEDGE_BASE_DATA_SOURCE_ID=… RAG_EVAL_KNOWLEDGE_BASE_TYPE=MANAGED \
//	  go test ./rag/internal/eval -run TestIdentifierRecall -v
func TestIdentifierRecall(t *testing.T) {
	databaseURL := os.Getenv("RAG_EVAL_DATABASE_URL")
	if databaseURL == "" || os.Getenv("RAG_EVAL_BACKEND") != string(domain.BackendBedrockKB) {
		t.Skip("RAG_EVAL_DATABASE_URL and RAG_EVAL_BACKEND=bedrock-kb are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("AWS configuration: %v", err)
	}
	documents, knowledgeBase, kind, reranks := knowledgeBaseFromEnv(t, awsConfig, os.Getenv("RAG_EVAL_RERANK_ARN"))
	vectors := vectorstore.NewRouter(vectorstore.BedrockKnowledgeBase{})
	embedders := embedding.NewRegistry(embedding.Config{})
	pipeline := ingest.New(store, vectors, embedders, documents, ingest.Options{}, logger)
	retriever := retrieval.New(store, vectors, embedders, nil, knowledgeBase, logger)

	organizationID := "org-ids-" + time.Now().UTC().Format("20060102150405")
	defer cleanup(t, store, organizationID)
	spec, _ := domain.BackendBedrockKB.RequiredEmbeddingSpec()
	corpus := createCorpus(t, ctx, store, organizationID, spec, domain.BackendBedrockKB, domain.DefaultChunkPolicy(spec))

	// Eight of each kind, with numbers from a fixed seed so every run asks
	// about the same messages.
	random := rand.New(rand.NewPCG(2026, 9))
	type message struct{ id, name, question string }
	var messages []message
	documentIDs := map[string]string{}
	defer func() {
		ids := make([]string, 0, len(documentIDs))
		for _, id := range documentIDs {
			ids = append(ids, id)
		}
		if err := documents.Delete(context.Background(), ids); err != nil {
			t.Errorf("delete the run's documents from the knowledge base: %v", err)
		}
	}()
	for _, template := range identifierMessages {
		for index := range 8 {
			identifier := template.id(random)
			name := fmt.Sprintf("%s-%d", template.kind, index)
			stored := storeDocument(t, ctx, store, corpus, name, fmt.Sprintf(template.body, identifier))
			if _, err := pipeline.Index(ctx, stored); err != nil {
				t.Fatalf("submit %s: %v", name, err)
			}
			documentIDs[name] = stored.ID
			messages = append(messages, message{id: identifier, name: name, question: fmt.Sprintf(template.question, identifier)})
		}
	}
	waitForKnowledgeBase(t, ctx, store, pipeline, organizationID, documentIDs)

	passes := []bool{false}
	if reranks {
		passes = append(passes, true)
	}
	for _, rerank := range passes {
		for _, form := range []string{"number alone", "question"} {
			hits := map[string]float64{}
			var reciprocal float64
			for _, entry := range messages {
				text := entry.id
				if form == "question" {
					text = entry.question
				}
				found, err := retriever.Search(ctx, corpus, domain.Query{Text: text, TopK: topK, Rerank: rerank})
				if err != nil {
					t.Fatalf("search %q: %v", text, err)
				}
				rank := 0
				for index, match := range found.Matches {
					if match.Chunk.DocumentID == documentIDs[entry.name] {
						rank = index + 1
						break
					}
				}
				score(hits, &reciprocal, rank)
			}
			label := form
			if rerank {
				label += ", reranked"
			}
			count := float64(len(messages))
			t.Logf("%s, %s: hit@1 %.2f @3 %.2f @8 %.2f MRR %.3f over %d messages",
				kind, label, hits["1"]/count, hits["3"]/count, hits["8"]/count, reciprocal/count, len(messages))
		}
	}
}
