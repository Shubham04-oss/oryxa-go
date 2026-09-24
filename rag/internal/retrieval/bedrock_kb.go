package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	kbdoc "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/document"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// KnowledgeBaseAPI is the part of Bedrock Agent Runtime this package uses to
// search a knowledge base.
type KnowledgeBaseAPI interface {
	Retrieve(context.Context, *bedrockagentruntime.RetrieveInput, ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RetrieveOutput, error)
}

// retrieveLimit bounds one Retrieve call. Rag over-fetches candidates for its
// own diversification the way it does against pgvector and S3 Vectors, but
// Bedrock refuses a request for more results than this, so the over-fetch is
// capped here rather than failing the query outright.
const retrieveLimit = 100

// KnowledgeBase searches Amazon Bedrock's own managed index directly.
//
// Bedrock chunks, embeds and vector-searches on rag's behalf, and can rerank
// too, so there is no dense/lexical split to fuse here the way there is for
// pgvector and S3 Vectors: one call already ranks the organization's own
// passages against the question. What still runs afterward, in
// Retriever.searchKnowledgeBase, is the same diversity, top_k and min_score
// handling every other corpus gets, so a caller sees one contract regardless
// of which backend a corpus happened to be created on.
//
// It speaks to either kind of knowledge base Bedrock has. A VECTOR one
// searches the index it was created over — S3 Vectors here, which Bedrock
// searches by meaning only — and reranks with RAG_RERANK_MODEL_ARN when
// asked. A MANAGED one keeps its own index, searches it by keyword and by
// meaning together on every query, and reranks with a model of its own that
// its retrieval price already covers. Both take documents the same way (see
// rag/internal/knowledgebase); only the search request differs.
type KnowledgeBase struct {
	client          KnowledgeBaseAPI
	knowledgeBaseID string
	// managed is a MANAGED knowledge base rather than a VECTOR one.
	managed bool
	// rerankModelARN reuses RAG_RERANK_MODEL_ARN, the same model the
	// cross-encoder Reranker uses for pgvector and S3 Vectors, so an operator
	// configures reranking once rather than once per backend. A managed
	// knowledge base never uses it.
	rerankModelARN string
}

// NewKnowledgeBase builds a retriever over one Bedrock knowledge base. rerankModelARN
// may be empty, in which case a query asking to rerank gets Bedrock's own
// unreranked vector-search order instead.
func NewKnowledgeBase(client KnowledgeBaseAPI, knowledgeBaseID, rerankModelARN string) (*KnowledgeBase, error) {
	if client == nil {
		return nil, errors.New("a Bedrock Agent Runtime client is required")
	}
	if strings.TrimSpace(knowledgeBaseID) == "" {
		return nil, errors.New("a knowledge base id is required")
	}
	return &KnowledgeBase{
		client: client, knowledgeBaseID: strings.TrimSpace(knowledgeBaseID), rerankModelARN: strings.TrimSpace(rerankModelARN),
	}, nil
}

// NewManagedKnowledgeBase builds a retriever over one MANAGED Bedrock
// knowledge base. It takes no reranking model: a managed knowledge base
// reranks with its own when a query asks it to.
func NewManagedKnowledgeBase(client KnowledgeBaseAPI, knowledgeBaseID string) (*KnowledgeBase, error) {
	knowledgeBase, err := NewKnowledgeBase(client, knowledgeBaseID, "")
	if err != nil {
		return nil, err
	}
	knowledgeBase.managed = true
	return knowledgeBase, nil
}

// reranks says whether a query that asks to rerank is reranked: always by a
// managed knowledge base, and by a vector one only when it was given a model.
func (knowledgeBase *KnowledgeBase) reranks() bool {
	return knowledgeBase.managed || knowledgeBase.rerankModelARN != ""
}

// tenantConditions is the metadata conditions that keep one organization's
// search from ever seeing another's passages, and one of its corpora's
// search from seeing another corpus's documents: organization_id must equal
// the caller's, and corpus_id must be (or, given more than one, be among)
// the corpora the caller may search. These are the only two facts every
// document in the knowledge base carries regardless of which rag corpus or
// organization put it there, since one knowledge base serves the whole
// deployment.
func tenantConditions(organizationID string, corpusIDs []string) []agenttypes.RetrievalFilter {
	conditions := []agenttypes.RetrievalFilter{
		&agenttypes.RetrievalFilterMemberEquals{Value: agenttypes.FilterAttribute{
			Key: aws.String("organization_id"), Value: kbdoc.NewLazyDocument(organizationID),
		}},
	}
	if len(corpusIDs) == 1 {
		// Equals rather than a one-element In: it is what the validated
		// behaviour calls for, and it reads as what it means.
		conditions = append(conditions, &agenttypes.RetrievalFilterMemberEquals{Value: agenttypes.FilterAttribute{
			Key: aws.String("corpus_id"), Value: kbdoc.NewLazyDocument(corpusIDs[0]),
		}})
	} else {
		conditions = append(conditions, &agenttypes.RetrievalFilterMemberIn{Value: agenttypes.FilterAttribute{
			Key: aws.String("corpus_id"), Value: kbdoc.NewLazyDocument(corpusIDs),
		}})
	}
	return conditions
}

// TenantFilter builds the metadata filter that keeps one organization's
// search from ever seeing another's passages, and one of its corpora's
// search from seeing another corpus's documents. It is exported so a test
// can assert on the exact filter this builds: tenant isolation is the one
// thing here that must never regress.
func TenantFilter(organizationID string, corpusIDs []string) agenttypes.RetrievalFilter {
	return &agenttypes.RetrievalFilterMemberAndAll{Value: tenantConditions(organizationID, corpusIDs)}
}

// maxFilterGroup is the most filters Bedrock takes in one andAll or orAll.
// Groups nest one level deep: the top-level andAll may hold groups of
// filters, and a group holds filters only.
const maxFilterGroup = 5

// maxCallerFilters is the most filters a caller's own query filter may turn
// into on a bedrock-kb corpus, a document list counting as one and each
// condition as one: as many as fit, in groups, beside what a member's search
// always carries at the top level (organization, corpus and access). It is
// the same whoever asks, so no filter works for an admin and fails for a
// member.
const maxCallerFilters = (maxFilterGroup - 3) * maxFilterGroup

// searchFilter narrows TenantFilter further by a caller's own query filter,
// when it named one, and to member-visible documents unless the caller may
// see admin-only ones.
//
// Only fields actually written by knowledgebase.Client.Submit are accepted.
// A condition on anything else is refused rather than silently widening a
// search. Tenant and access filters are always added independently, at the
// top level; the caller's go there too while every list stays within
// maxFilterGroup, and in nested groups otherwise.
func searchFilter(organizationID string, corpusIDs []string, filter domain.Filter, includeAdminOnly bool) (agenttypes.RetrievalFilter, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	var caller []agenttypes.RetrievalFilter
	if len(filter.DocumentIDs) > 0 {
		caller = append(caller, documentIDCondition(filter.DocumentIDs))
	}
	for _, condition := range filter.Conditions {
		if !kbFilterable(condition.Key) {
			return nil, fmt.Errorf("a bedrock-kb corpus cannot filter by metadata key %q: %w", condition.Key, domain.ErrInvalid)
		}
		attribute := agenttypes.FilterAttribute{Key: aws.String(condition.Key)}
		switch condition.Operator {
		case domain.OperatorEqual:
			attribute.Value = kbdoc.NewLazyDocument(condition.Values[0])
			caller = append(caller, &agenttypes.RetrievalFilterMemberEquals{Value: attribute})
		case domain.OperatorNotEqual:
			attribute.Value = kbdoc.NewLazyDocument(condition.Values[0])
			caller = append(caller, &agenttypes.RetrievalFilterMemberNotEquals{Value: attribute})
		case domain.OperatorIn:
			attribute.Value = kbdoc.NewLazyDocument(condition.Values)
			caller = append(caller, &agenttypes.RetrievalFilterMemberIn{Value: attribute})
		case domain.OperatorNotIn:
			attribute.Value = kbdoc.NewLazyDocument(condition.Values)
			caller = append(caller, &agenttypes.RetrievalFilterMemberNotIn{Value: attribute})
		default:
			return nil, fmt.Errorf("a bedrock-kb corpus cannot use operator %q: %w", condition.Operator, domain.ErrInvalid)
		}
	}
	if len(caller) > maxCallerFilters {
		return nil, fmt.Errorf("a bedrock-kb corpus takes at most %d filter conditions, a document list counting as one: %w",
			maxCallerFilters, domain.ErrInvalid)
	}
	conditions := tenantConditions(organizationID, corpusIDs)
	if !includeAdminOnly {
		conditions = append(conditions, &agenttypes.RetrievalFilterMemberEquals{Value: agenttypes.FilterAttribute{
			Key: aws.String("access"), Value: kbdoc.NewLazyDocument(string(domain.AccessMembers)),
		}})
	}
	conditions = append(conditions, grouped(caller, maxFilterGroup-len(conditions))...)
	return &agenttypes.RetrievalFilterMemberAndAll{Value: conditions}, nil
}

// grouped fits filters into at most slots entries of an andAll: each on its
// own while they all fit, and otherwise in nested andAll groups of at most
// maxFilterGroup, as even in size as they divide — never a group of one,
// which Bedrock refuses. searchFilter's limit on the caller's filters is what
// guarantees the groups fit.
func grouped(filters []agenttypes.RetrievalFilter, slots int) []agenttypes.RetrievalFilter {
	if len(filters) <= slots {
		return filters
	}
	count := (len(filters) + maxFilterGroup - 1) / maxFilterGroup
	groups := make([]agenttypes.RetrievalFilter, 0, count)
	for index := range count {
		start, end := index*len(filters)/count, (index+1)*len(filters)/count
		groups = append(groups, &agenttypes.RetrievalFilterMemberAndAll{Value: filters[start:end]})
	}
	return groups
}

func kbFilterable(key string) bool {
	switch key {
	case "source_kind", "provider", "knowledge_source_id", "document_kind", "category":
		return true
	default:
		return false
	}
}

func documentIDCondition(documentIDs []string) agenttypes.RetrievalFilter {
	if len(documentIDs) == 1 {
		return &agenttypes.RetrievalFilterMemberEquals{Value: agenttypes.FilterAttribute{
			Key: aws.String("document_id"), Value: kbdoc.NewLazyDocument(documentIDs[0]),
		}}
	}
	return &agenttypes.RetrievalFilterMemberIn{Value: agenttypes.FilterAttribute{
		Key: aws.String("document_id"), Value: kbdoc.NewLazyDocument(documentIDs),
	}}
}

// kbPassage is one chunk Bedrock's own retrieval already found, ranked and,
// optionally, reranked. Rag never sees or invents the chunking underneath it.
type kbPassage struct {
	documentID string
	text       string
	score      float64
}

// retrieve asks the knowledge base for the best passages across the named
// corpora of one organization. numberOfResults is the candidate pool size —
// callers cut it down afterward the same way they would for any other
// backend's over-fetch.
func (knowledgeBase *KnowledgeBase) retrieve(ctx context.Context, organizationID string, corpusIDs []string, query domain.Query, numberOfResults int) ([]kbPassage, error) {
	if len(corpusIDs) == 0 {
		return nil, nil
	}
	numberOfResults = min(max(numberOfResults, 1), retrieveLimit)
	filter, err := searchFilter(organizationID, corpusIDs, query.Filter, query.IncludeAdminOnly)
	if err != nil {
		return nil, err
	}
	output, err := knowledgeBase.client.Retrieve(ctx, &bedrockagentruntime.RetrieveInput{
		KnowledgeBaseId:        aws.String(knowledgeBase.knowledgeBaseID),
		RetrievalQuery:         &agenttypes.KnowledgeBaseQuery{Text: aws.String(query.Text)},
		RetrievalConfiguration: knowledgeBase.retrievalConfiguration(filter, numberOfResults, query.Rerank),
	})
	if err != nil {
		return nil, fmt.Errorf("retrieve from the knowledge base: %w", err)
	}
	passages := make([]kbPassage, 0, len(output.RetrievalResults))
	for _, result := range output.RetrievalResults {
		if result.Content == nil || result.Content.Text == nil || strings.TrimSpace(*result.Content.Text) == "" {
			// Rag only indexes and answers from text. A result of another
			// content type (image, row, audio, video) has nothing here to
			// put in a domain.Match, so it is left out rather than returned
			// empty — the same reason rag refuses those content types at
			// ingest for every other backend.
			continue
		}
		if result.Location == nil || result.Location.CustomDocumentLocation == nil || result.Location.CustomDocumentLocation.Id == nil {
			// Every document rag ingests is CUSTOM, identified by rag's own
			// document id (see knowledgebase.Client.Submit). A result naming
			// no such identifier did not come from anything rag wrote.
			continue
		}
		var score float64
		if result.Score != nil {
			score = *result.Score
		}
		passages = append(passages, kbPassage{
			documentID: *result.Location.CustomDocumentLocation.Id,
			text:       *result.Content.Text,
			score:      score,
		})
	}
	return passages, nil
}

// retrievalConfiguration is the one part of a Retrieve request that differs
// between the two kinds of knowledge base: both get the same tenant filter
// and the same number of results, and only the configuration they travel in,
// and how reranking is asked for, changes.
func (knowledgeBase *KnowledgeBase) retrievalConfiguration(filter agenttypes.RetrievalFilter, numberOfResults int, rerank bool) *agenttypes.KnowledgeBaseRetrievalConfiguration {
	if knowledgeBase.managed {
		// Reranking is named either way rather than left to the service's
		// default, so a query that did not ask to rerank does not wait on the
		// reranker: the same contract every other backend keeps.
		reranking := agenttypes.RerankingModelTypeNone
		if rerank {
			reranking = agenttypes.RerankingModelTypeManaged
		}
		return &agenttypes.KnowledgeBaseRetrievalConfiguration{
			ManagedSearchConfiguration: &agenttypes.ManagedSearchConfiguration{
				NumberOfResults:    aws.Int32(int32(numberOfResults)),
				Filter:             filter,
				RerankingModelType: reranking,
			},
		}
	}
	vectorSearch := &agenttypes.KnowledgeBaseVectorSearchConfiguration{
		NumberOfResults: aws.Int32(int32(numberOfResults)),
		Filter:          filter,
	}
	if rerank && knowledgeBase.rerankModelARN != "" {
		vectorSearch.RerankingConfiguration = &agenttypes.VectorSearchRerankingConfiguration{
			Type: agenttypes.VectorSearchRerankingConfigurationTypeBedrockRerankingModel,
			BedrockRerankingConfiguration: &agenttypes.VectorSearchBedrockRerankingConfiguration{
				ModelConfiguration:      &agenttypes.VectorSearchBedrockRerankingModelConfiguration{ModelArn: aws.String(knowledgeBase.rerankModelARN)},
				NumberOfRerankedResults: aws.Int32(int32(numberOfResults)),
			},
		}
	}
	return &agenttypes.KnowledgeBaseRetrievalConfiguration{VectorSearchConfiguration: vectorSearch}
}

// kbChunkID derives a stable id for a passage that never had a row in
// rag_chunks: the same passage from the same document gets the same id
// across queries, which is what makes a citation stable to look at twice.
// There is no byte range to give it — nothing here knows where in the
// document Bedrock found this text — so callers leave StartByte and EndByte
// at zero.
func kbChunkID(documentID, text string) string {
	sum := sha256.Sum256([]byte(documentID + "\x00" + text))
	return domain.ChunkIDPrefix + "kb_" + hex.EncodeToString(sum[:12])
}
