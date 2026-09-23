package knowledgebase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagent/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func testCorpus() domain.Corpus {
	return domain.Corpus{ID: "cor_1", OrganizationID: "org_1", Backend: domain.BackendBedrockKB, Spec: domain.BedrockKnowledgeBaseEmbeddingSpec}
}

func testDocument() domain.Document {
	return domain.Document{
		ID: "doc_1", CorpusID: "cor_1", OrganizationID: "org_1",
		Source: domain.Source{Kind: domain.SourceInline}, Title: "Runbook",
		ContentSHA256: "hash-1",
	}
}

// fakeIngest records the last call each method received, and answers what a
// test configured.
type fakeIngest struct {
	lastIngest *bedrockagent.IngestKnowledgeBaseDocumentsInput
	ingestErr  error

	lastGet   *bedrockagent.GetKnowledgeBaseDocumentsInput
	getOutput *bedrockagent.GetKnowledgeBaseDocumentsOutput
	getErr    error

	deleteCalls []*bedrockagent.DeleteKnowledgeBaseDocumentsInput
	deleteErr   error
}

func (fake *fakeIngest) IngestKnowledgeBaseDocuments(_ context.Context, input *bedrockagent.IngestKnowledgeBaseDocumentsInput, _ ...func(*bedrockagent.Options)) (*bedrockagent.IngestKnowledgeBaseDocumentsOutput, error) {
	fake.lastIngest = input
	if fake.ingestErr != nil {
		return nil, fake.ingestErr
	}
	return &bedrockagent.IngestKnowledgeBaseDocumentsOutput{}, nil
}

func (fake *fakeIngest) GetKnowledgeBaseDocuments(_ context.Context, input *bedrockagent.GetKnowledgeBaseDocumentsInput, _ ...func(*bedrockagent.Options)) (*bedrockagent.GetKnowledgeBaseDocumentsOutput, error) {
	fake.lastGet = input
	if fake.getErr != nil {
		return nil, fake.getErr
	}
	if fake.getOutput != nil {
		return fake.getOutput, nil
	}
	return &bedrockagent.GetKnowledgeBaseDocumentsOutput{}, nil
}

func (fake *fakeIngest) DeleteKnowledgeBaseDocuments(_ context.Context, input *bedrockagent.DeleteKnowledgeBaseDocumentsInput, _ ...func(*bedrockagent.Options)) (*bedrockagent.DeleteKnowledgeBaseDocumentsOutput, error) {
	fake.deleteCalls = append(fake.deleteCalls, input)
	if fake.deleteErr != nil {
		return nil, fake.deleteErr
	}
	return &bedrockagent.DeleteKnowledgeBaseDocumentsOutput{}, nil
}

// fakeObjects stands in for S3, recording what was staged and deleted.
type fakeObjects struct {
	put       []*s3.PutObjectInput
	putErr    error
	deleted   []*s3.DeleteObjectInput
	deleteErr error
}

func (fake *fakeObjects) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	fake.put = append(fake.put, input)
	if fake.putErr != nil {
		return nil, fake.putErr
	}
	return &s3.PutObjectOutput{}, nil
}

func (fake *fakeObjects) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	fake.deleted = append(fake.deleted, input)
	if fake.deleteErr != nil {
		return nil, fake.deleteErr
	}
	return &s3.DeleteObjectOutput{}, nil
}

func testConfig() Config {
	return Config{KnowledgeBaseID: "kb-1", DataSourceID: "ds-1", Bucket: "oryxa-rag-staging"}
}

func attribute(attributes []agenttypes.MetadataAttribute, key string) (string, bool) {
	for _, attribute := range attributes {
		if aws.ToString(attribute.Key) == key {
			return aws.ToString(attribute.Value.StringValue), true
		}
	}
	return "", false
}

func TestNewValidatesItsInputs(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, nil, testConfig()); err == nil {
		t.Fatal("a nil Bedrock Agent client was accepted")
	}
	if _, err := New(&fakeIngest{}, nil, Config{DataSourceID: "ds-1"}); err == nil {
		t.Fatal("a missing knowledge base id was accepted")
	}
	if _, err := New(&fakeIngest{}, nil, Config{KnowledgeBaseID: "kb-1"}); err == nil {
		t.Fatal("a missing data source id was accepted")
	}
	if _, err := New(&fakeIngest{}, nil, testConfig()); err != nil {
		t.Fatalf("a workable config was refused: %v", err)
	}
}

func TestSubmitSendsTextInline(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{}
	client, err := New(ingest, nil, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Submit(context.Background(), testCorpus(), testDocument(), "small enough to send inline"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := aws.ToString(ingest.lastIngest.KnowledgeBaseId); got != "kb-1" {
		t.Fatalf("knowledge base id = %q", got)
	}
	if got := aws.ToString(ingest.lastIngest.DataSourceId); got != "ds-1" {
		t.Fatalf("data source id = %q", got)
	}
	if len(ingest.lastIngest.Documents) != 1 {
		t.Fatalf("submitted %d documents, want 1", len(ingest.lastIngest.Documents))
	}
	content := ingest.lastIngest.Documents[0].Content
	if content.DataSourceType != agenttypes.ContentDataSourceTypeCustom {
		t.Fatalf("data source type = %q, want CUSTOM", content.DataSourceType)
	}
	custom := content.Custom
	if custom == nil || aws.ToString(custom.CustomDocumentIdentifier.Id) != "doc_1" {
		t.Fatalf("custom document id = %+v, want doc_1", custom)
	}
	if custom.SourceType != agenttypes.CustomSourceTypeInLine {
		t.Fatalf("source type = %q, want IN_LINE", custom.SourceType)
	}
	if got := aws.ToString(custom.InlineContent.TextContent.Data); got != "small enough to send inline" {
		t.Fatalf("inline text = %q", got)
	}
	metadata := ingest.lastIngest.Documents[0].Metadata
	if metadata.Type != agenttypes.MetadataSourceTypeInLineAttribute {
		t.Fatalf("metadata type = %q, want IN_LINE_ATTRIBUTE", metadata.Type)
	}
	for key, want := range map[string]string{
		"organization_id": "org_1", "corpus_id": "cor_1", "document_id": "doc_1", "title": "Runbook", "access": "members",
	} {
		if got, ok := attribute(metadata.InlineAttributes, key); !ok || got != want {
			t.Fatalf("attribute %q = %q, %v; want %q", key, got, ok, want)
		}
	}
}

func TestSubmitIndexesKnowledgeProvenanceWithoutProviderDetails(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{}
	client, err := New(ingest, nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	document := testDocument()
	document.Metadata = domain.Metadata{
		"provider": "gmail", "knowledge_source_id": "src_1", "document_kind": "message",
		"category": "financial_record", "from": "private@example.com",
	}
	if err := client.Submit(context.Background(), testCorpus(), document, "invoice details"); err != nil {
		t.Fatal(err)
	}
	attributes := ingest.lastIngest.Documents[0].Metadata.InlineAttributes
	for key, want := range map[string]string{
		"provider": "gmail", "knowledge_source_id": "src_1", "document_kind": "message", "category": "financial_record",
	} {
		if got, ok := attribute(attributes, key); !ok || got != want {
			t.Fatalf("%s = %q, %v; want %q", key, got, ok, want)
		}
	}
	if _, ok := attribute(attributes, "from"); ok {
		t.Fatal("a Gmail address was copied to Bedrock metadata")
	}
}

func TestSubmitStagesTextOverTheInlineLimit(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{}
	objects := &fakeObjects{}
	client, err := New(ingest, objects, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	large := strings.Repeat("x", inlineLimit+1)
	document := testDocument()
	document.ContentSHA256 = "big-hash"
	if err := client.Submit(context.Background(), testCorpus(), document, large); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(objects.put) != 1 {
		t.Fatalf("put %d objects, want 1", len(objects.put))
	}
	wantKey := StagingKey("org_1", "cor_1", "doc_1", "big-hash")
	if got := aws.ToString(objects.put[0].Key); got != wantKey {
		t.Fatalf("staged key = %q, want %q", got, wantKey)
	}
	if got := aws.ToString(objects.put[0].Bucket); got != "oryxa-rag-staging" {
		t.Fatalf("staged bucket = %q", got)
	}
	custom := ingest.lastIngest.Documents[0].Content.Custom
	if custom.SourceType != agenttypes.CustomSourceTypeS3Location {
		t.Fatalf("source type = %q, want S3_LOCATION", custom.SourceType)
	}
	wantURI := "s3://oryxa-rag-staging/" + wantKey
	if got := aws.ToString(custom.S3Location.Uri); got != wantURI {
		t.Fatalf("staged uri = %q, want %q", got, wantURI)
	}
}

func TestSubmitRefusesToStageWithNoBucketConfigured(t *testing.T) {
	t.Parallel()
	client, err := New(&fakeIngest{}, nil, Config{KnowledgeBaseID: "kb-1", DataSourceID: "ds-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	large := strings.Repeat("x", inlineLimit+1)
	err = client.Submit(context.Background(), testCorpus(), testDocument(), large)
	if err == nil {
		t.Fatal("a document too large to send inline was accepted with no staging bucket")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want it to wrap domain.ErrInvalid", err)
	}
}

// TestSubmitOriginalPointsAtItsOwnBytes: a document with no extracted text
// goes straight to its own object, regardless of what body text is passed —
// there is none to consider — and it is never staged, since it is already
// exactly where it needs to be.
func TestSubmitOriginalPointsAtItsOwnBytes(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{}
	objects := &fakeObjects{}
	client, err := New(ingest, objects, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document := testDocument()
	document.OriginalBucket, document.OriginalKey = "oryxa-knowledge-originals", "org/doc/handbook.pdf"
	if err := client.Submit(context.Background(), testCorpus(), document, ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	custom := ingest.lastIngest.Documents[0].Content.Custom
	if custom.SourceType != agenttypes.CustomSourceTypeS3Location {
		t.Fatalf("source type = %q, want S3_LOCATION", custom.SourceType)
	}
	if got, want := aws.ToString(custom.S3Location.Uri), "s3://oryxa-knowledge-originals/org/doc/handbook.pdf"; got != want {
		t.Fatalf("uri = %q, want %q", got, want)
	}
	if len(objects.put) != 0 {
		t.Fatal("an original document was staged, but it was already in S3")
	}
}

func TestStatusesMapsByCustomIDAndFillsInNotFound(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{getOutput: &bedrockagent.GetKnowledgeBaseDocumentsOutput{
		DocumentDetails: []agenttypes.KnowledgeBaseDocumentDetail{{
			Identifier: &agenttypes.DocumentIdentifier{Custom: &agenttypes.CustomDocumentIdentifier{Id: aws.String("doc_1")}},
			Status:     agenttypes.DocumentStatusIndexed,
		}, {
			Identifier:   &agenttypes.DocumentIdentifier{Custom: &agenttypes.CustomDocumentIdentifier{Id: aws.String("doc_2")}},
			Status:       agenttypes.DocumentStatusFailed,
			StatusReason: aws.String("unsupported content type"),
		}},
	}}
	client, err := New(ingest, nil, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	statuses, err := client.Statuses(context.Background(), []string{"doc_1", "doc_2", "doc_3"})
	if err != nil {
		t.Fatalf("Statuses: %v", err)
	}
	if statuses["doc_1"].Code != "INDEXED" {
		t.Fatalf("doc_1 status = %+v", statuses["doc_1"])
	}
	if statuses["doc_2"].Code != "FAILED" || statuses["doc_2"].Reason != "unsupported content type" {
		t.Fatalf("doc_2 status = %+v", statuses["doc_2"])
	}
	// doc_3 was asked about but never mentioned in the response, which is
	// what a document the knowledge base has never heard of looks like.
	if statuses["doc_3"].Code != "NOT_FOUND" {
		t.Fatalf("doc_3 status = %+v, want NOT_FOUND", statuses["doc_3"])
	}
}

func TestDeleteBatchesOver25Documents(t *testing.T) {
	t.Parallel()
	ingest := &fakeIngest{}
	client, err := New(ingest, nil, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ids := make([]string, 0, 40)
	for index := 0; index < 40; index++ {
		ids = append(ids, fmt.Sprintf("doc_%d", index))
	}
	if err := client.Delete(context.Background(), ids); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(ingest.deleteCalls) != 2 {
		t.Fatalf("made %d delete calls, want 2 batches for 40 ids", len(ingest.deleteCalls))
	}
	if len(ingest.deleteCalls[0].DocumentIdentifiers) != 25 || len(ingest.deleteCalls[1].DocumentIdentifiers) != 15 {
		t.Fatalf("batch sizes = %d, %d; want 25 then 15",
			len(ingest.deleteCalls[0].DocumentIdentifiers), len(ingest.deleteCalls[1].DocumentIdentifiers))
	}
}

func TestDeleteStagedIsANoOpWithoutABucket(t *testing.T) {
	t.Parallel()
	client, err := New(&fakeIngest{}, nil, Config{KnowledgeBaseID: "kb-1", DataSourceID: "ds-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.DeleteStaged(context.Background(), testCorpus(), testDocument()); err != nil {
		t.Fatalf("DeleteStaged with no bucket configured: %v", err)
	}
}

func TestDeleteStagedRemovesTheStagedObject(t *testing.T) {
	t.Parallel()
	objects := &fakeObjects{}
	client, err := New(&fakeIngest{}, objects, testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document := testDocument()
	if err := client.DeleteStaged(context.Background(), testCorpus(), document); err != nil {
		t.Fatalf("DeleteStaged: %v", err)
	}
	if len(objects.deleted) != 1 {
		t.Fatalf("deleted %d objects, want 1", len(objects.deleted))
	}
	wantKey := StagingKey("org_1", "cor_1", "doc_1", document.ContentSHA256)
	if got := aws.ToString(objects.deleted[0].Key); got != wantKey {
		t.Fatalf("deleted key = %q, want %q", got, wantKey)
	}
}
