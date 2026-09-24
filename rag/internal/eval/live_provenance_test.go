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
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgebase"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// TestLiveManagedProvenance is an opt-in, billed smoke test of the production
// ingestion, retrieval and generation components. It uses synthetic text and
// removes its Bedrock documents even when an assertion fails.
func TestLiveManagedProvenance(t *testing.T) {
	if os.Getenv("RAG_LIVE_PROVENANCE") != "1" {
		t.Skip("set RAG_LIVE_PROVENANCE=1 to call Bedrock and Vertex AI")
	}
	databaseURL := os.Getenv("RAG_EVAL_DATABASE_URL")
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	kbID := os.Getenv("RAG_EVAL_KNOWLEDGE_BASE_ID")
	dataSourceID := os.Getenv("RAG_EVAL_KNOWLEDGE_BASE_DATA_SOURCE_ID")
	if databaseURL == "" || project == "" || kbID == "" || dataSourceID == "" {
		t.Fatal("RAG_EVAL_DATABASE_URL, GOOGLE_CLOUD_PROJECT, RAG_EVAL_KNOWLEDGE_BASE_ID and RAG_EVAL_KNOWLEDGE_BASE_DATA_SOURCE_ID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
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
	documents, err := knowledgebase.New(bedrockagent.NewFromConfig(awsConfig), nil,
		knowledgebase.Config{KnowledgeBaseID: kbID, DataSourceID: dataSourceID})
	if err != nil {
		t.Fatal(err)
	}
	knowledgeBase, err := retrieval.NewManagedKnowledgeBase(bedrockagentruntime.NewFromConfig(awsConfig), kbID)
	if err != nil {
		t.Fatal(err)
	}
	vectors := vectorstore.NewRouter(vectorstore.BedrockKnowledgeBase{})
	embedders := embedding.NewRegistry(embedding.Config{})
	pipeline := ingest.New(store, vectors, embedders, documents, ingest.Options{}, logger)
	searcher := retrieval.New(store, vectors, embedders, nil, knowledgeBase, logger)
	organizationID := fmt.Sprintf("org-live-%d", time.Now().UTC().UnixNano())
	defer cleanup(t, store, organizationID)
	var documentIDs []string
	defer func() {
		if len(documentIDs) == 0 {
			return
		}
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer deleteCancel()
		if err := documents.Delete(deleteCtx, documentIDs); err != nil {
			t.Errorf("remove synthetic Bedrock documents: %v", err)
		}
	}()
	spec, _ := domain.BackendBedrockKB.RequiredEmbeddingSpec()
	corpus := createCorpus(t, ctx, store, organizationID, spec, domain.BackendBedrockKB, domain.DefaultChunkPolicy(spec))
	marker := fmt.Sprintf("ORYXALIVE%d", time.Now().UTC().UnixNano())
	invoiceBody := fmt.Sprintf("Invoice %s for September design services. Total due: 128 dollars by October 15. Payment status: unpaid.", marker)
	invoice := liveDocument(t, ctx, store, corpus, "Gmail invoice "+marker, invoiceBody,
		domain.AccessMembers, domain.Metadata{"provider": "gmail", "knowledge_source_id": "src_live_gmail", "document_kind": "message", "category": "financial_record"})
	documentIDs = append(documentIDs, invoice.ID)
	adminBody := fmt.Sprintf("Private payroll note %s: the internal payroll approval code is 7319.", marker)
	admin := liveDocument(t, ctx, store, corpus, "Admin payroll "+marker, adminBody,
		domain.AccessAdmins, domain.Metadata{"provider": "files", "knowledge_source_id": "src_live_files", "document_kind": "file", "category": "work_material"})
	documentIDs = append(documentIDs, admin.ID)
	for _, document := range []domain.Document{invoice, admin} {
		if _, err := pipeline.Index(ctx, document); err != nil {
			t.Fatalf("submit %s: %v", document.ID, err)
		}
	}
	waitForKnowledgeBase(t, ctx, store, pipeline, organizationID, map[string]string{"invoice": invoice.ID, "admin": admin.ID})

	financialFilter := domain.Filter{Conditions: []domain.Condition{
		{Key: "provider", Operator: domain.OperatorEqual, Values: []string{"gmail"}},
		{Key: "category", Operator: domain.OperatorEqual, Values: []string{"financial_record"}},
	}}
	question := "What is the payment status and amount due for invoice " + marker + "?"
	matches := awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: question, TopK: 4, Filter: financialFilter}, invoice.ID)
	if matches[0].Document.Metadata["provider"] != "gmail" || matches[0].Chunk.Metadata["category"] != "financial_record" ||
		domain.Citations(matches)[0].Metadata["provider"] != "gmail" {
		t.Fatalf("retrieved passage or citation lost provenance: %+v", matches[0])
	}
	stranger := corpus
	stranger.OrganizationID = organizationID + "-stranger"
	foreign, err := searcher.Search(ctx, stranger, domain.Query{Text: question, TopK: 4})
	if err != nil || len(foreign.Matches) != 0 {
		t.Fatalf("another organization retrieved passages: %+v, %v", foreign.Matches, err)
	}
	fileFilter := domain.Filter{Conditions: []domain.Condition{{Key: "provider", Operator: domain.OperatorEqual, Values: []string{"files"}}}}
	adminQuestion := "What is the payroll approval code in " + marker + "?"
	// The admin-only document is shown to be searchable first: a member
	// finding nothing proves nothing about access until it is.
	awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: adminQuestion, TopK: 4, Filter: fileFilter, IncludeAdminOnly: true}, admin.ID)
	memberResult, err := searcher.Search(ctx, corpus, domain.Query{Text: adminQuestion, TopK: 4, Filter: fileFilter})
	if err != nil || len(memberResult.Matches) != 0 {
		t.Fatalf("member retrieved admin-only document: %+v, %v", memberResult.Matches, err)
	}

	model := os.Getenv("RAG_LIVE_ANSWER_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	answerer, err := generation.NewVertex(ctx, generation.VertexConfig{Project: project, Location: "global", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := answerer.Generate(ctx, generation.Request{
		Question: question, Passages: generation.PassagesFrom(matches), MaxTokens: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.Text, "128") || len(generation.Cited(answer.Text, len(matches))) == 0 {
		t.Fatalf("Gemini did not answer from and cite the invoice: %q", answer.Text)
	}
	t.Logf("Gemini answered the synthetic invoice with a citation using %d input and %d output tokens", answer.Usage.InputTokens, answer.Usage.OutputTokens)

	updated := invoice
	updated.Metadata = domain.Metadata{"provider": "gmail", "knowledge_source_id": "src_live_gmail", "document_kind": "message", "category": "account_record"}
	updated.UpdatedAt = time.Now().UTC()
	updated, _, err = store.UpsertDocument(ctx, updated, invoiceBody, event(t, "rag.document.received.v1", organizationID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.Index(ctx, updated); err != nil {
		t.Fatalf("resubmit metadata-only change: %v", err)
	}
	waitForKnowledgeBase(t, ctx, store, pipeline, organizationID, map[string]string{"updated invoice": invoice.ID})
	newFilter := domain.Filter{Conditions: []domain.Condition{{Key: "category", Operator: domain.OperatorEqual, Values: []string{"account_record"}}}}
	updatedMatches := awaitLiveMatch(t, ctx, searcher, corpus, domain.Query{Text: question, TopK: 4, Filter: newFilter}, invoice.ID)
	if updatedMatches[0].Document.Metadata["category"] != "account_record" {
		t.Fatalf("updated match has stale category: %+v", updatedMatches[0].Document.Metadata)
	}
	oldCategoryDeadline := time.Now().Add(time.Minute)
	for {
		old, err := searcher.Search(ctx, corpus, domain.Query{Text: question, TopK: 4, Filter: financialFilter})
		if err != nil {
			t.Fatal(err)
		}
		if len(old.Matches) == 0 {
			break
		}
		if time.Now().After(oldCategoryDeadline) {
			t.Fatalf("old category still returns the reclassified document: %+v", old.Matches)
		}
		time.Sleep(5 * time.Second)
	}
	t.Log("Bedrock indexed the metadata-only category update and returned the reclassified document")
}

func liveDocument(t *testing.T, ctx context.Context, store *postgres.Store, corpus domain.Corpus, title, body string,
	access domain.DocumentAccess, metadata domain.Metadata) domain.Document {
	t.Helper()
	id, err := domain.NewID(domain.DocumentIDPrefix)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	document, _, err := store.UpsertDocument(ctx, domain.Document{
		ID: id, CorpusID: corpus.ID, OrganizationID: corpus.OrganizationID,
		Source: domain.Source{Kind: domain.SourceExternal, Ref: "https://example.invalid/" + id},
		Title:  title, ContentType: "text/plain", ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)),
		Metadata: metadata, Access: access, CreatedBy: "usr_eval", CreatedAt: now, UpdatedAt: now,
	}, body, event(t, "rag.document.received.v1", corpus.OrganizationID))
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func awaitLiveMatch(t *testing.T, ctx context.Context, searcher *retrieval.Retriever, corpus domain.Corpus,
	query domain.Query, documentID string) []domain.Match {
	t.Helper()
	var last []domain.Match
	for {
		result, err := searcher.Search(ctx, corpus, query)
		if err != nil {
			t.Fatal(err)
		}
		last = result.Matches
		for _, match := range last {
			if match.Document.ID == documentID {
				return last
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Bedrock did not return document %s; last matches: %+v", documentID, last)
		case <-time.After(5 * time.Second):
		}
	}
}
