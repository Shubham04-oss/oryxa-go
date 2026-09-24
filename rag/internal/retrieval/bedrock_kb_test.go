package retrieval

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// filterValue unwraps what NewLazyDocument wrapped, so a test can compare it
// directly rather than against an opaque document.Interface. Every document
// this package builds is created by NewLazyDocument, whose concrete type
// exposes the original value through this method, unexported package or not:
// Go does not gate a method by where its type was defined.
func filterValue(t *testing.T, value any) any {
	t.Helper()
	valuer, ok := value.(interface{ Value() any })
	if !ok {
		t.Fatalf("%#v does not expose its wrapped value", value)
	}
	return valuer.Value()
}

// TestTenantFilterSingleCorpus is the filter a search against one corpus
// sends. Tenant isolation depends on exactly this shape — organization_id
// and corpus_id both required, both compared with Equals — so it is asserted
// field by field rather than merely "some filter came out".
func TestTenantFilterSingleCorpus(t *testing.T) {
	t.Parallel()
	filter := TenantFilter("org_1", []string{"cor_1"})
	andAll, ok := filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok {
		t.Fatalf("filter = %T, want *RetrievalFilterMemberAndAll", filter)
	}
	if len(andAll.Value) != 2 {
		t.Fatalf("AndAll has %d conditions, want 2", len(andAll.Value))
	}
	org, ok := andAll.Value[0].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(org.Value.Key) != "organization_id" || filterValue(t, org.Value.Value) != "org_1" {
		t.Fatalf("first condition = %#v, want Equals(organization_id, org_1)", andAll.Value[0])
	}
	corpus, ok := andAll.Value[1].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(corpus.Value.Key) != "corpus_id" || filterValue(t, corpus.Value.Value) != "cor_1" {
		t.Fatalf("second condition = %#v, want Equals(corpus_id, cor_1)", andAll.Value[1])
	}
}

// TestTenantFilterMultipleCorpora: naming several corpora uses In rather
// than repeating Equals, exactly as the validated behaviour calls for.
func TestTenantFilterMultipleCorpora(t *testing.T) {
	t.Parallel()
	filter := TenantFilter("org_1", []string{"cor_1", "cor_2"})
	andAll, ok := filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok || len(andAll.Value) != 2 {
		t.Fatalf("filter = %#v", filter)
	}
	if _, ok := andAll.Value[0].(*agenttypes.RetrievalFilterMemberEquals); !ok {
		t.Fatalf("first condition = %T, want organization_id Equals", andAll.Value[0])
	}
	corpus, ok := andAll.Value[1].(*agenttypes.RetrievalFilterMemberIn)
	if !ok || aws.ToString(corpus.Value.Key) != "corpus_id" {
		t.Fatalf("second condition = %#v, want In(corpus_id, ...)", andAll.Value[1])
	}
	got, ok := filterValue(t, corpus.Value.Value).([]string)
	if !ok || len(got) != 2 || got[0] != "cor_1" || got[1] != "cor_2" {
		t.Fatalf("corpus_id values = %#v, want [cor_1 cor_2]", filterValue(t, corpus.Value.Value))
	}
}

// TestSearchFilterAddsDocumentIDsWithinTheTenantFilter: a caller's own
// document_ids filter narrows further, it does not replace, the tenant
// conditions that must always be present.
func TestSearchFilterAddsDocumentIDsWithinTheTenantFilter(t *testing.T) {
	t.Parallel()
	filter, err := searchFilter("org_1", []string{"cor_1"}, domain.Filter{DocumentIDs: []string{"doc_1"}}, true)
	if err != nil {
		t.Fatalf("searchFilter: %v", err)
	}
	andAll, ok := filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok || len(andAll.Value) != 3 {
		t.Fatalf("filter = %#v, want the tenant's 2 conditions plus one for document_id", filter)
	}
	document, ok := andAll.Value[2].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(document.Value.Key) != "document_id" || filterValue(t, document.Value.Value) != "doc_1" {
		t.Fatalf("third condition = %#v, want Equals(document_id, doc_1)", andAll.Value[2])
	}
}

// TestSearchFilterRefusesMetadataConditions: rag never writes a caller's
// arbitrary document metadata onto a knowledge base attribute, only a fixed,
// small set (see knowledgebase.Client.Submit), so a condition on anything
// else must be refused rather than silently return more than was asked for.
func TestSearchFilterRefusesMetadataConditions(t *testing.T) {
	t.Parallel()
	_, err := searchFilter("org_1", []string{"cor_1"}, domain.Filter{
		Conditions: []domain.Condition{{Key: "team", Operator: domain.OperatorEqual, Values: []string{"platform"}}},
	}, true)
	if err == nil {
		t.Fatal("a metadata condition was silently accepted for a bedrock-kb corpus")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestSearchFilterNarrowsByKnowledgeProvenance(t *testing.T) {
	t.Parallel()
	filter, err := searchFilter("org_1", []string{"cor_1"}, domain.Filter{Conditions: []domain.Condition{
		{Key: "provider", Operator: domain.OperatorEqual, Values: []string{"gmail"}},
		{Key: "category", Operator: domain.OperatorIn, Values: []string{"booking", "financial_record"}},
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	all := filter.(*agenttypes.RetrievalFilterMemberAndAll).Value
	if len(all) != 4 {
		t.Fatalf("conditions = %d, want tenant pair and provenance pair", len(all))
	}
	provider, ok := all[2].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(provider.Value.Key) != "provider" || filterValue(t, provider.Value.Value) != "gmail" {
		t.Fatalf("provider filter = %#v", all[2])
	}
	category, ok := all[3].(*agenttypes.RetrievalFilterMemberIn)
	if !ok || aws.ToString(category.Value.Key) != "category" {
		t.Fatalf("category filter = %#v", all[3])
	}
}

// fakeKnowledgeBaseAPI stands in for Bedrock Agent Runtime's Retrieve call.
type fakeKnowledgeBaseAPI struct {
	lastInput *bedrockagentruntime.RetrieveInput
	output    *bedrockagentruntime.RetrieveOutput
	err       error
}

func (fake *fakeKnowledgeBaseAPI) Retrieve(_ context.Context, input *bedrockagentruntime.RetrieveInput, _ ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RetrieveOutput, error) {
	fake.lastInput = input
	if fake.err != nil {
		return nil, fake.err
	}
	if fake.output != nil {
		return fake.output, nil
	}
	return &bedrockagentruntime.RetrieveOutput{}, nil
}

func textResult(documentID, text string, score float64) agenttypes.KnowledgeBaseRetrievalResult {
	return agenttypes.KnowledgeBaseRetrievalResult{
		Content: &agenttypes.RetrievalResultContent{Text: aws.String(text)},
		Location: &agenttypes.RetrievalResultLocation{
			Type:                   agenttypes.RetrievalResultLocationTypeCustom,
			CustomDocumentLocation: &agenttypes.RetrievalResultCustomDocumentLocation{Id: aws.String(documentID)},
		},
		Score: aws.Float64(score),
	}
}

func TestKnowledgeBaseRetrieveMapsResultsAndSkipsWhatItCannotUse(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{output: &bedrockagentruntime.RetrieveOutput{RetrievalResults: []agenttypes.KnowledgeBaseRetrievalResult{
		textResult("doc_1", "credentials rotate every ninety days", 0.9),
		// No text: an image or row result rag has nothing to embed into a
		// domain.Match, so it must not turn into an empty passage.
		{Content: &agenttypes.RetrievalResultContent{Type: agenttypes.RetrievalResultContentTypeImage}, Score: aws.Float64(0.8)},
		// No custom document location: not something rag itself ingested.
		{Content: &agenttypes.RetrievalResultContent{Text: aws.String("from somewhere else")}, Score: aws.Float64(0.7)},
	}}}
	knowledgeBase, err := NewKnowledgeBase(api, "kb-1", "")
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	passages, err := knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "how does rotation work?"}, 20)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(passages) != 1 {
		t.Fatalf("got %d passages, want 1: %+v", len(passages), passages)
	}
	if passages[0].documentID != "doc_1" || passages[0].score != 0.9 {
		t.Fatalf("passage = %+v", passages[0])
	}
	if got := aws.ToString(api.lastInput.KnowledgeBaseId); got != "kb-1" {
		t.Fatalf("knowledge base id = %q", got)
	}
	if got := aws.ToInt32(api.lastInput.RetrievalConfiguration.VectorSearchConfiguration.NumberOfResults); got != 20 {
		t.Fatalf("number of results = %d, want 20", got)
	}
	if api.lastInput.RetrievalConfiguration.VectorSearchConfiguration.RerankingConfiguration != nil {
		t.Fatal("reranking was configured when the query never asked for it")
	}
}

func TestKnowledgeBaseRetrieveClampsNumberOfResults(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{}
	knowledgeBase, err := NewKnowledgeBase(api, "kb-1", "")
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	if _, err := knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "q"}, 400); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if got := aws.ToInt32(api.lastInput.RetrievalConfiguration.VectorSearchConfiguration.NumberOfResults); got != retrieveLimit {
		t.Fatalf("number of results = %d, want it clamped to %d", got, retrieveLimit)
	}
}

func TestKnowledgeBaseRetrieveConfiguresRerankingWhenAsked(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{}
	knowledgeBase, err := NewKnowledgeBase(api, "kb-1", "arn:aws:bedrock:ap-south-1::foundation-model/cohere.rerank-v3-5:0")
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	if _, err := knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "q", Rerank: true}, 10); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	reranking := api.lastInput.RetrievalConfiguration.VectorSearchConfiguration.RerankingConfiguration
	if reranking == nil {
		t.Fatal("Rerank: true did not configure reranking")
	}
	if reranking.Type != agenttypes.VectorSearchRerankingConfigurationTypeBedrockRerankingModel {
		t.Fatalf("reranking type = %q", reranking.Type)
	}
	if got := aws.ToString(reranking.BedrockRerankingConfiguration.ModelConfiguration.ModelArn); got != "arn:aws:bedrock:ap-south-1::foundation-model/cohere.rerank-v3-5:0" {
		t.Fatalf("reranking model arn = %q", got)
	}
}

// TestManagedKnowledgeBaseSearchesThroughManagedSearch: a MANAGED knowledge
// base refuses a vector search configuration, so its request must carry the
// same tenant filter and the same clamped number of results in a managed
// search configuration instead, and nothing in the vector one.
func TestManagedKnowledgeBaseSearchesThroughManagedSearch(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{output: &bedrockagentruntime.RetrieveOutput{RetrievalResults: []agenttypes.KnowledgeBaseRetrievalResult{
		textResult("doc_1", "invoice INV-2026-48213 is due on the first", 0.7),
	}}}
	knowledgeBase, err := NewManagedKnowledgeBase(api, "kb-managed")
	if err != nil {
		t.Fatalf("NewManagedKnowledgeBase: %v", err)
	}
	passages, err := knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "INV-2026-48213"}, 400)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(passages) != 1 || passages[0].documentID != "doc_1" {
		t.Fatalf("passages = %+v, want doc_1's", passages)
	}
	configuration := api.lastInput.RetrievalConfiguration
	if configuration.VectorSearchConfiguration != nil {
		t.Fatal("a managed knowledge base was sent a vector search configuration")
	}
	managed := configuration.ManagedSearchConfiguration
	if managed == nil {
		t.Fatal("a managed knowledge base was sent no managed search configuration")
	}
	if got := aws.ToInt32(managed.NumberOfResults); got != retrieveLimit {
		t.Fatalf("number of results = %d, want it clamped to %d", got, retrieveLimit)
	}
	andAll, ok := managed.Filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok || len(andAll.Value) != 3 {
		t.Fatalf("filter = %#v, want tenant and member ACL filters", managed.Filter)
	}
	org, ok := andAll.Value[0].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(org.Value.Key) != "organization_id" || filterValue(t, org.Value.Value) != "org_1" {
		t.Fatalf("first condition = %#v, want Equals(organization_id, org_1)", andAll.Value[0])
	}
	corpus, ok := andAll.Value[1].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(corpus.Value.Key) != "corpus_id" || filterValue(t, corpus.Value.Value) != "cor_1" {
		t.Fatalf("second condition = %#v, want Equals(corpus_id, cor_1)", andAll.Value[1])
	}
	access, ok := andAll.Value[2].(*agenttypes.RetrievalFilterMemberEquals)
	if !ok || aws.ToString(access.Value.Key) != "access" || filterValue(t, access.Value.Value) != "members" {
		t.Fatalf("third condition = %#v, want Equals(access, members)", andAll.Value[2])
	}
	// Not asked to rerank, so reranking is turned off by name rather than
	// left to whatever the service would choose.
	if managed.RerankingModelType != agenttypes.RerankingModelTypeNone || managed.RerankingConfiguration != nil {
		t.Fatalf("reranking = %q %+v, want NONE", managed.RerankingModelType, managed.RerankingConfiguration)
	}
}

func TestManagedKnowledgeBaseAdminQueryIncludesRestrictedDocuments(t *testing.T) {
	api := &fakeKnowledgeBaseAPI{}
	knowledgeBase, err := NewManagedKnowledgeBase(api, "kb-managed")
	if err != nil {
		t.Fatal(err)
	}
	_, err = knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "payroll", IncludeAdminOnly: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	filter := api.lastInput.RetrievalConfiguration.ManagedSearchConfiguration.Filter
	andAll, ok := filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok || len(andAll.Value) != 2 {
		t.Fatalf("admin filter = %#v, want only org and corpus", filter)
	}
}

func TestManagedKnowledgeBaseReranksWithItsOwnModelWhenAsked(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{}
	knowledgeBase, err := NewManagedKnowledgeBase(api, "kb-managed")
	if err != nil {
		t.Fatalf("NewManagedKnowledgeBase: %v", err)
	}
	if _, err := knowledgeBase.retrieve(context.Background(), "org_1", []string{"cor_1"}, domain.Query{Text: "q", Rerank: true}, 10); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	managed := api.lastInput.RetrievalConfiguration.ManagedSearchConfiguration
	if managed == nil || managed.RerankingModelType != agenttypes.RerankingModelTypeManaged {
		t.Fatalf("managed search = %+v, want the managed reranker", managed)
	}
	if managed.RerankingConfiguration != nil {
		t.Fatal("a managed reranker was given a model configuration of its own")
	}
}

// TestSearchManagedKnowledgeBaseReportsReranking: a managed knowledge base
// reranks with no RAG_RERANK_MODEL_ARN at all, so a search that asked to
// rerank is reported as reranked, and one that did not is not.
func TestSearchManagedKnowledgeBaseReportsReranking(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{output: &bedrockagentruntime.RetrieveOutput{RetrievalResults: []agenttypes.KnowledgeBaseRetrievalResult{
		textResult("doc_1", "first passage", 0.9),
	}}}
	knowledgeBase, err := NewManagedKnowledgeBase(api, "kb-managed")
	if err != nil {
		t.Fatalf("NewManagedKnowledgeBase: %v", err)
	}
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, knowledgeBase, nil)
	corpus := testCorpus()
	corpus.Backend = domain.BackendBedrockKB
	for _, rerank := range []bool{true, false} {
		results, err := retriever.Search(context.Background(), corpus, domain.Query{Text: "q", Rerank: rerank})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if results.Reranked != rerank {
			t.Fatalf("rerank %v: Reranked = %v", rerank, results.Reranked)
		}
		if len(results.Matches) != 1 {
			t.Fatalf("rerank %v: got %d matches, want 1", rerank, len(results.Matches))
		}
	}
}

// TestSearchKnowledgeBaseHydratesFromPostgresAndHonoursTopK exercises the
// whole path Retriever.Search takes for a bedrock-kb corpus: it must not
// touch the dense or lexical retrievers at all, and it must still apply
// top_k and min_score the same way the pgvector and S3 Vectors path does.
func TestSearchKnowledgeBaseHydratesFromPostgresAndHonoursTopK(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{output: &bedrockagentruntime.RetrieveOutput{RetrievalResults: []agenttypes.KnowledgeBaseRetrievalResult{
		textResult("doc_1", "first passage", 0.95),
		textResult("doc_2", "second passage", 0.80),
		textResult("doc_3", "third passage", 0.10),
	}}}
	knowledgeBase, err := NewKnowledgeBase(api, "kb-1", "")
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	chunks := &fakeChunks{metadata: map[string]domain.Metadata{"doc_1": {"provider": "gmail", "category": "financial_record"}}}
	vectors := &fakeVectorStore{}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, knowledgeBase, nil)

	corpus := testCorpus()
	corpus.Backend = domain.BackendBedrockKB
	results, err := retriever.Search(context.Background(), corpus, domain.Query{Text: "q", TopK: 2, MinScore: 0.5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if vectors.calls != 0 {
		t.Fatal("Search reached the vector store for a bedrock-kb corpus")
	}
	if chunks.lexicalCall != 0 {
		t.Fatal("Search ran a lexical search for a bedrock-kb corpus, which has no chunk table to search")
	}
	// top_k of 2 keeps the first two; min_score of 0.5 would have dropped
	// the third even without it.
	if len(results.Matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(results.Matches))
	}
	if results.Matches[0].Chunk.DocumentID != "doc_1" || results.Matches[0].Document.Title != "Title doc_1" {
		t.Fatalf("first match = %+v, want it hydrated from PostgreSQL's document refs", results.Matches[0])
	}
	if results.Matches[0].Document.Metadata["provider"] != "gmail" || results.Matches[0].Chunk.Metadata["category"] != "financial_record" ||
		domain.Citations(results.Matches)[0].Metadata["provider"] != "gmail" {
		t.Fatalf("provenance was not returned with match and citation: %+v", results.Matches[0])
	}
	// The chunk id is derived, not random, so the same passage cites the
	// same way if it is retrieved again.
	again := kbChunkID("doc_1", "first passage")
	if results.Matches[0].Chunk.ID != again {
		t.Fatalf("chunk id = %q, want the stable derived id %q", results.Matches[0].Chunk.ID, again)
	}
	if results.Matches[0].Chunk.StartByte != 0 || results.Matches[0].Chunk.EndByte != 0 {
		t.Fatalf("a knowledge base match carries a byte range: %+v", results.Matches[0].Chunk)
	}
}

// TestSearchKnowledgeBaseLeavesOutDocumentsRagNoLongerHolds: the knowledge
// base finishes a delete in its own time, after rag has already removed the
// document's row, so a search in between must not hand back the removed
// document's text — and top_k is still filled from what remains.
func TestSearchKnowledgeBaseLeavesOutDocumentsRagNoLongerHolds(t *testing.T) {
	t.Parallel()
	api := &fakeKnowledgeBaseAPI{output: &bedrockagentruntime.RetrieveOutput{RetrievalResults: []agenttypes.KnowledgeBaseRetrievalResult{
		textResult("doc_1", "first passage", 0.95),
		textResult("doc_removed", "a passage from a removed document", 0.90),
		textResult("doc_removed", "another passage from it", 0.85),
		textResult("doc_3", "third passage", 0.80),
	}}}
	knowledgeBase, err := NewManagedKnowledgeBase(api, "kb-managed")
	if err != nil {
		t.Fatalf("NewManagedKnowledgeBase: %v", err)
	}
	chunks := &fakeChunks{goneDocuments: map[string]bool{"doc_removed": true}}
	retriever := New(chunks, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, knowledgeBase, nil)
	corpus := testCorpus()
	corpus.Backend = domain.BackendBedrockKB

	results, err := retriever.Search(context.Background(), corpus, domain.Query{Text: "q", TopK: 2, Rerank: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results.Matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(results.Matches))
	}
	for index, want := range []string{"doc_1", "doc_3"} {
		match := results.Matches[index]
		if match.Chunk.DocumentID != want || match.Document.ID != want {
			t.Fatalf("match %d = %+v, want %s with its document ref", index, match, want)
		}
	}
}

// TestSearchKnowledgeBaseRefusesAMetadataFilter is the same refusal, exercised
// through Retriever.Search end to end rather than searchFilter directly.
func TestSearchKnowledgeBaseRefusesAMetadataFilter(t *testing.T) {
	t.Parallel()
	knowledgeBase, err := NewKnowledgeBase(&fakeKnowledgeBaseAPI{}, "kb-1", "")
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, knowledgeBase, nil)
	corpus := testCorpus()
	corpus.Backend = domain.BackendBedrockKB
	_, err = retriever.Search(context.Background(), corpus, domain.Query{
		Text: "q", Filter: domain.Filter{Conditions: []domain.Condition{{Key: "team", Operator: domain.OperatorEqual, Values: []string{"platform"}}}},
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestSearchKnowledgeBaseFailsByNameWithoutOne(t *testing.T) {
	t.Parallel()
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, nil, nil)
	corpus := testCorpus()
	corpus.Backend = domain.BackendBedrockKB
	_, err := retriever.Search(context.Background(), corpus, domain.Query{Text: "q"})
	if err == nil {
		t.Fatal("a bedrock-kb corpus was searched with no knowledge base configured")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want it to wrap domain.ErrInvalid", err)
	}
}

// filterLeaves flattens a filter into its comparisons, checking on the way
// the shape Bedrock takes: at most maxFilterGroup filters in any andAll, at
// least two, and groups nested one level deep at most.
func filterLeaves(t *testing.T, filter agenttypes.RetrievalFilter, depth int) []string {
	t.Helper()
	andAll, ok := filter.(*agenttypes.RetrievalFilterMemberAndAll)
	if !ok {
		switch leaf := filter.(type) {
		case *agenttypes.RetrievalFilterMemberEquals:
			return []string{"equals:" + aws.ToString(leaf.Value.Key)}
		case *agenttypes.RetrievalFilterMemberNotEquals:
			return []string{"notEquals:" + aws.ToString(leaf.Value.Key)}
		case *agenttypes.RetrievalFilterMemberIn:
			return []string{"in:" + aws.ToString(leaf.Value.Key)}
		case *agenttypes.RetrievalFilterMemberNotIn:
			return []string{"notIn:" + aws.ToString(leaf.Value.Key)}
		default:
			t.Fatalf("unexpected filter %T", filter)
			return nil
		}
	}
	if depth > 1 {
		t.Fatalf("an andAll nested %d levels deep; Bedrock takes one", depth)
	}
	if len(andAll.Value) < 2 || len(andAll.Value) > maxFilterGroup {
		t.Fatalf("an andAll of %d filters; Bedrock takes 2 to %d", len(andAll.Value), maxFilterGroup)
	}
	var leaves []string
	for _, member := range andAll.Value {
		leaves = append(leaves, filterLeaves(t, member, depth+1)...)
	}
	return leaves
}

// Bedrock takes at most five filters in one andAll, one level of nesting
// deep. A member's search already spends three on organization, corpus and
// access, so a flat list of a caller's conditions beside them was refused
// as soon as there were three. Every filter a caller may send now fits, for
// a member and an admin alike, and one that cannot fit is refused by name.
func TestSearchFilterKeepsEveryListWithinWhatBedrockTakes(t *testing.T) {
	t.Parallel()
	keys := []string{"provider", "knowledge_source_id", "document_kind", "category", "source_kind"}
	for _, includeAdminOnly := range []bool{false, true} {
		for callerFilters := 0; callerFilters <= maxCallerFilters; callerFilters++ {
			var filter domain.Filter
			want := []string{"equals:organization_id", "equals:corpus_id"}
			if !includeAdminOnly {
				want = append(want, "equals:access")
			}
			conditions := callerFilters
			if callerFilters > 0 {
				filter.DocumentIDs = []string{"doc_1", "doc_2"}
				want = append(want, "in:document_id")
				conditions--
			}
			for index := range conditions {
				key := keys[index%len(keys)]
				filter.Conditions = append(filter.Conditions, domain.Condition{Key: key, Operator: domain.OperatorNotIn, Values: []string{"x"}})
				want = append(want, "notIn:"+key)
			}
			built, err := searchFilter("org_1", []string{"cor_1"}, filter, includeAdminOnly)
			if err != nil {
				t.Fatalf("admin=%v with %d filters: %v", includeAdminOnly, callerFilters, err)
			}
			got := filterLeaves(t, built, 0)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("admin=%v with %d filters: leaves %v, want %v", includeAdminOnly, callerFilters, got, want)
			}
		}
		tooMany := domain.Filter{DocumentIDs: []string{"doc_1"}}
		for index := range maxCallerFilters {
			tooMany.Conditions = append(tooMany.Conditions, domain.Condition{Key: keys[index%len(keys)], Operator: domain.OperatorEqual, Values: []string{"x"}})
		}
		if _, err := searchFilter("org_1", []string{"cor_1"}, tooMany, includeAdminOnly); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("admin=%v with %d filters = %v, want ErrInvalid", includeAdminOnly, maxCallerFilters+1, err)
		}
	}
}
