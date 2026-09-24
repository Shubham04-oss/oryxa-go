package eval

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// TestLiveS3VectorsConverse is opt-in because it creates a temporary S3
// Vectors index and calls billed Titan and Converse models. It deletes the
// index when the test finishes, including after a failed assertion.
func TestLiveS3VectorsConverse(t *testing.T) {
	if os.Getenv("RAG_LIVE_S3VECTORS") != "1" {
		t.Skip("set RAG_LIVE_S3VECTORS=1 to call S3 Vectors and Bedrock models")
	}
	databaseURL, bucket := os.Getenv("RAG_EVAL_DATABASE_URL"), os.Getenv("RAG_EVAL_VECTOR_BUCKET")
	if databaseURL == "" || bucket == "" {
		t.Fatal("RAG_EVAL_DATABASE_URL and RAG_EVAL_VECTOR_BUCKET are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vectorClient, err := vectorstore.NewS3Vectors(s3vectors.NewFromConfig(awsConfig), bucket)
	if err != nil {
		t.Fatal(err)
	}
	vectors := vectorstore.NewRouter(vectorClient)
	modelClient := bedrockruntime.NewFromConfig(awsConfig)
	embedders := embedding.NewRegistry(embedding.Config{Bedrock: modelClient, Options: embedding.Options{Logger: logger}})
	pipeline := ingest.New(store, vectors, embedders, nil, ingest.Options{}, logger)
	searcher := retrieval.New(store, vectors, embedders, nil, nil, logger)
	organizationID := fmt.Sprintf("org-s3live-%d", time.Now().UTC().UnixNano())
	defer cleanup(t, store, organizationID)
	spec := domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 1024, Metric: domain.MetricCosine}
	corpus := createCorpus(t, ctx, store, organizationID, spec, domain.BackendS3Vectors, domain.DefaultChunkPolicy(spec))
	defer func() {
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), time.Minute)
		defer deleteCancel()
		if err := vectorClient.DropCorpus(deleteCtx, corpus); err != nil {
			t.Errorf("remove the temporary S3 Vectors index: %v", err)
		}
	}()
	marker := fmt.Sprintf("ORYXAS3LIVE%d", time.Now().UTC().UnixNano())
	invoiceBody := fmt.Sprintf("Invoice %s for September design services. The amount due is 128 dollars by October 15. It remains unpaid.", marker)
	invoice := liveDocument(t, ctx, store, corpus, "Gmail invoice "+marker, invoiceBody,
		domain.AccessMembers, domain.Metadata{"provider": "gmail", "document_kind": "message", "category": "financial_record"})
	adminBody := fmt.Sprintf("Private payroll note %s: the internal payroll approval code is 7319.", marker)
	admin := liveDocument(t, ctx, store, corpus, "Admin payroll "+marker, adminBody,
		domain.AccessAdmins, domain.Metadata{"provider": "files", "document_kind": "file", "category": "work_material"})
	for _, document := range []domain.Document{invoice, admin} {
		if _, err := pipeline.Index(ctx, document); err != nil {
			t.Fatalf("index %s: %v", document.ID, err)
		}
	}
	invoiceQuestion := "How much is due for invoice " + marker + "?"
	financialFilter := domain.Filter{Conditions: []domain.Condition{
		{Key: "provider", Operator: domain.OperatorEqual, Values: []string{"gmail"}},
		{Key: "category", Operator: domain.OperatorEqual, Values: []string{"financial_record"}},
	}}
	matches := awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: invoiceQuestion, TopK: 4, Mode: domain.ModeDense, Filter: financialFilter}, invoice.ID)
	if matches[0].Document.Metadata["provider"] != "gmail" || domain.Citations(matches)[0].Metadata["category"] != "financial_record" {
		t.Fatalf("S3 Vectors match lost provenance: %+v", matches[0])
	}
	stranger := corpus
	stranger.OrganizationID = organizationID + "-stranger"
	foreign, err := searcher.Search(ctx, stranger, domain.Query{Text: invoiceQuestion, TopK: 4, Mode: domain.ModeDense})
	if err != nil || len(foreign.Matches) != 0 {
		t.Fatalf("another organization retrieved S3 Vectors passages: %+v, %v", foreign.Matches, err)
	}
	fileFilter := domain.Filter{Conditions: []domain.Condition{{Key: "provider", Operator: domain.OperatorEqual, Values: []string{"files"}}}}
	adminQuestion := "What is the payroll approval code in " + marker + "?"
	// The admin-only passage is shown to be searchable first: a member
	// finding nothing proves nothing about access until it is.
	awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: adminQuestion, TopK: 4, Mode: domain.ModeDense, Filter: fileFilter, IncludeAdminOnly: true}, admin.ID)
	member, err := searcher.Search(ctx, corpus, domain.Query{Text: adminQuestion, TopK: 4, Mode: domain.ModeDense, Filter: fileFilter})
	if err != nil || len(member.Matches) != 0 {
		t.Fatalf("member retrieved an admin-only S3 Vectors passage: %+v, %v", member.Matches, err)
	}

	model := os.Getenv("RAG_LIVE_CONVERSE_MODEL")
	if model == "" {
		model = "us.amazon.nova-micro-v1:0"
	}
	answerer, err := generation.NewBedrock(modelClient, model, generation.BedrockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.Generate(ctx, generation.Request{
		Question: invoiceQuestion, Passages: generation.PassagesFrom(matches), MaxTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Text, "128") || len(generation.Cited(answer.Text, len(matches))) == 0 {
		t.Fatalf("Converse did not answer from and cite the S3 Vectors passage: %q", answer.Text)
	}
	t.Logf("Titan plus S3 Vectors plus Converse answered with a citation using %d input and %d output tokens", answer.Usage.InputTokens, answer.Usage.OutputTokens)

	updated := invoice
	updated.Metadata = domain.Metadata{"provider": "gmail", "document_kind": "message", "category": "account_record"}
	updated.UpdatedAt = time.Now().UTC()
	updated, _, err = store.UpsertDocument(ctx, updated, invoiceBody, event(t, "rag.document.received.v1", organizationID))
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := pipeline.Index(ctx, updated)
	if err != nil {
		t.Fatalf("reindex metadata-only change: %v", err)
	}
	// The same chunks are written again with the new metadata, from the
	// vectors S3 Vectors already holds: none is removed, none embedded anew.
	if len(indexed.Sync.Fresh) == 0 || len(indexed.Sync.Removed) != 0 || indexed.Usage.EmbeddingTokens != 0 {
		t.Fatalf("metadata-only update did not rewrite the chunks in place: %+v, usage %+v", indexed.Sync, indexed.Usage)
	}
	newFilter := domain.Filter{Conditions: []domain.Condition{{Key: "category", Operator: domain.OperatorEqual, Values: []string{"account_record"}}}}
	awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: invoiceQuestion, TopK: 4, Mode: domain.ModeDense, Filter: newFilter}, invoice.ID)
	old, err := searcher.Search(ctx, corpus, domain.Query{Text: invoiceQuestion, TopK: 4, Mode: domain.ModeDense, Filter: financialFilter})
	if err != nil || len(old.Matches) != 0 {
		t.Fatalf("old category still matched after S3 vector update: %+v, %v", old.Matches, err)
	}
	t.Log("S3 Vectors replaced the category metadata and the old filter returned no passages")
}
