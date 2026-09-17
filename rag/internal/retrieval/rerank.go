package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	agenttypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
)

// Scored is one passage's relevance to the question, by its position in the
// list that was handed to the reranker.
type Scored struct {
	Index int
	Score float64
}

// Reranker reads the question and a passage together and says how well the
// passage answers it.
//
// This is a different question from the one retrieval asks. A bi-encoder has
// to place a passage in the space before it knows what will be asked, so it
// can only say what the passage is about. A cross-encoder sees both at once,
// so it can tell a passage about credential rotation from the one that says
// how often it happens.
type Reranker interface {
	Rerank(ctx context.Context, query string, passages []string, topN int) ([]Scored, error)
}

// RerankAPI is the part of Bedrock Agent Runtime this package uses.
type RerankAPI interface {
	Rerank(context.Context, *bedrockagentruntime.RerankInput, ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error)
}

// rerankBatchSize bounds one Rerank call. The service limits how many sources
// a single request may carry, and a candidate pool is capped well below this,
// so in practice this is a guard rather than a loop that runs.
const rerankBatchSize = 100

// BedrockReranker reranks with a Bedrock reranking model, such as Cohere
// Rerank or Amazon Rerank.
type BedrockReranker struct {
	client   RerankAPI
	modelARN string
}

// NewBedrockReranker builds a reranker over a model ARN.
//
// An ARN rather than a model id, because that is what the Rerank API takes and
// because it is region-qualified: the reranker has to live where the service
// runs, and naming it fully means a misconfiguration is visible in the
// configuration rather than at the first query.
func NewBedrockReranker(client RerankAPI, modelARN string) (*BedrockReranker, error) {
	if client == nil {
		return nil, errors.New("a Bedrock Agent Runtime client is required")
	}
	if !strings.HasPrefix(strings.TrimSpace(modelARN), "arn:") {
		return nil, fmt.Errorf("a reranking model must be named by ARN, got %q", modelARN)
	}
	return &BedrockReranker{client: client, modelARN: strings.TrimSpace(modelARN)}, nil
}

func (reranker *BedrockReranker) Rerank(ctx context.Context, query string, passages []string, topN int) ([]Scored, error) {
	if len(passages) == 0 {
		return nil, nil
	}
	if topN < 1 || topN > len(passages) {
		topN = len(passages)
	}
	if len(passages) > rerankBatchSize {
		return nil, fmt.Errorf("reranking %d passages at once is over the limit of %d", len(passages), rerankBatchSize)
	}
	sources := make([]agenttypes.RerankSource, 0, len(passages))
	for _, passage := range passages {
		sources = append(sources, agenttypes.RerankSource{
			Type: agenttypes.RerankSourceTypeInline,
			InlineDocumentSource: &agenttypes.RerankDocument{
				Type:         agenttypes.RerankDocumentTypeText,
				TextDocument: &agenttypes.RerankTextDocument{Text: aws.String(passage)},
			},
		})
	}
	output, err := reranker.client.Rerank(ctx, &bedrockagentruntime.RerankInput{
		Queries: []agenttypes.RerankQuery{{
			Type:      agenttypes.RerankQueryContentTypeText,
			TextQuery: &agenttypes.RerankTextDocument{Text: aws.String(query)},
		}},
		Sources: sources,
		RerankingConfiguration: &agenttypes.RerankingConfiguration{
			Type: agenttypes.RerankingConfigurationTypeBedrockRerankingModel,
			BedrockRerankingConfiguration: &agenttypes.BedrockRerankingConfiguration{
				NumberOfResults: aws.Int32(int32(topN)),
				ModelConfiguration: &agenttypes.BedrockRerankingModelConfiguration{
					ModelArn: aws.String(reranker.modelARN),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}
	scored := make([]Scored, 0, len(output.Results))
	for _, result := range output.Results {
		if result.Index == nil {
			continue
		}
		var relevance float64
		if result.RelevanceScore != nil {
			relevance = float64(*result.RelevanceScore)
		}
		scored = append(scored, Scored{Index: int(*result.Index), Score: relevance})
	}
	return scored, nil
}
