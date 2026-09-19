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
type KnowledgeBase struct {
	client          KnowledgeBaseAPI
	knowledgeBaseID string
	// rerankModelARN reuses RAG_RERANK_MODEL_ARN, the same model the
	// cross-encoder Reranker uses for pgvector and S3 Vectors, so an operator
	// configures reranking once rather than once per backend.
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

// searchFilter narrows TenantFilter further by a caller's own query filter,
// when it named one.
//
// Only document_id is honoured: it is the one metadata key rag always writes
// onto a document in the knowledge base (see knowledgebase.Client.Submit),
// so it is the only one a filter here can mean anything by. A condition on
// any other key is refused rather than silently dropped — a caller who asked
// for a narrower search getting a wider one back, unannounced, is a worse
// failure than an error naming exactly what this backend cannot do.
func searchFilter(organizationID string, corpusIDs []string, filter domain.Filter) (agenttypes.RetrievalFilter, error) {
	conditions := tenantConditions(organizationID, corpusIDs)
	if len(filter.Conditions) > 0 {
		return nil, fmt.Errorf("a bedrock-kb corpus can only filter by document id, not metadata key %q: %w",
			filter.Conditions[0].Key, domain.ErrInvalid)
	}
	if len(filter.DocumentIDs) > 0 {
		conditions = append(conditions, documentIDCondition(filter.DocumentIDs))
	}
	return &agenttypes.RetrievalFilterMemberAndAll{Value: conditions}, nil
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
	filter, err := searchFilter(organizationID, corpusIDs, query.Filter)
	if err != nil {
		return nil, err
	}
	vectorSearch := &agenttypes.KnowledgeBaseVectorSearchConfiguration{
		NumberOfResults: aws.Int32(int32(numberOfResults)),
		Filter:          filter,
	}
	if query.Rerank && knowledgeBase.rerankModelARN != "" {
		vectorSearch.RerankingConfiguration = &agenttypes.VectorSearchRerankingConfiguration{
			Type: agenttypes.VectorSearchRerankingConfigurationTypeBedrockRerankingModel,
			BedrockRerankingConfiguration: &agenttypes.VectorSearchBedrockRerankingConfiguration{
				ModelConfiguration:      &agenttypes.VectorSearchBedrockRerankingModelConfiguration{ModelArn: aws.String(knowledgeBase.rerankModelARN)},
				NumberOfRerankedResults: aws.Int32(int32(numberOfResults)),
			},
		}
	}
	output, err := knowledgeBase.client.Retrieve(ctx, &bedrockagentruntime.RetrieveInput{
		KnowledgeBaseId: aws.String(knowledgeBase.knowledgeBaseID),
		RetrievalQuery:  &agenttypes.KnowledgeBaseQuery{Text: aws.String(query.Text)},
		RetrievalConfiguration: &agenttypes.KnowledgeBaseRetrievalConfiguration{
			VectorSearchConfiguration: vectorSearch,
		},
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
