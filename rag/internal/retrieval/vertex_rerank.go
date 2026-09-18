package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// DefaultVertexRankingModel is Google's general-purpose semantic ranker.
const DefaultVertexRankingModel = "semantic-ranker-default@latest"

// vertexRankLimit is the most records one ranking call accepts.
const vertexRankLimit = 200

// VertexRerankConfig says which project pays for ranking and which model does
// it.
type VertexRerankConfig struct {
	Project string
	// Model defaults to DefaultVertexRankingModel.
	Model string
	// HTTPClient carries the credentials. Leave it nil and application
	// default credentials are used; tests set it to reach a stub.
	HTTPClient *http.Client
	// Endpoint overrides the API host, for tests.
	Endpoint string
}

// VertexReranker reranks with Google's Ranking API, the cross-encoder behind
// Vertex AI Search. It is serverless — billed per call, with nothing to
// provision — and runs from any location against the global ranking config.
//
// Measured over this repository's own documentation (rag/internal/eval), it
// is the largest single gain in the retrieval path: put in front of Vertex AI
// RAG Engine it moved the answer's section into first place for 77% of
// questions rather than 62%.
type VertexReranker struct {
	client   *http.Client
	project  string
	model    string
	endpoint string
}

// NewVertexReranker builds a reranker over the Ranking API.
func NewVertexReranker(ctx context.Context, config VertexRerankConfig) (*VertexReranker, error) {
	project := strings.TrimSpace(config.Project)
	if project == "" {
		return nil, errors.New("a Google Cloud project is required: the Ranking API bills one")
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultVertexRankingModel
	}
	client := config.HTTPClient
	if client == nil {
		source, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("find Google application default credentials: %w", err)
		}
		client = oauth2.NewClient(ctx, source)
		client.Timeout = 20 * time.Second
	}
	host := strings.TrimRight(config.Endpoint, "/")
	if host == "" {
		host = "https://discoveryengine.googleapis.com"
	}
	return &VertexReranker{
		client:  client,
		project: project,
		model:   model,
		endpoint: fmt.Sprintf("%s/v1/projects/%s/locations/global/rankingConfigs/default_ranking_config:rank",
			host, url.PathEscape(project)),
	}, nil
}

type vertexRankRecord struct {
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
}

type vertexRankRequest struct {
	Model   string             `json:"model"`
	Query   string             `json:"query"`
	Records []vertexRankRecord `json:"records"`
	TopN    int                `json:"topN"`
	// The passages are already in hand, so the response need only say which
	// and how well; echoing them back would ship the document text twice.
	IgnoreRecordDetails bool `json:"ignoreRecordDetailsInResponse"`
}

type vertexRankResponse struct {
	Records []struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	} `json:"records"`
}

func (reranker *VertexReranker) Rerank(ctx context.Context, query string, passages []string, topN int) ([]Scored, error) {
	if len(passages) == 0 {
		return nil, nil
	}
	if topN < 1 || topN > len(passages) {
		topN = len(passages)
	}
	if len(passages) > vertexRankLimit {
		return nil, fmt.Errorf("ranking %d passages at once is over the limit of %d", len(passages), vertexRankLimit)
	}
	records := make([]vertexRankRecord, 0, len(passages))
	for index, passage := range passages {
		records = append(records, vertexRankRecord{ID: strconv.Itoa(index), Content: passage})
	}
	body, err := json.Marshal(vertexRankRequest{
		Model: reranker.model, Query: query, Records: records, TopN: topN, IgnoreRecordDetails: true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode ranking request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, reranker.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build ranking request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	// User credentials name no project of their own, and the Ranking API
	// refuses a call that does not say which project it is for.
	request.Header.Set("X-Goog-User-Project", reranker.project)
	response, err := reranker.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call the Ranking API: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		// The body is not echoed: an error can quote the query or a passage.
		return nil, fmt.Errorf("the Ranking API returned %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	var decoded vertexRankResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode ranking response: %w", err)
	}
	scored := make([]Scored, 0, len(decoded.Records))
	for _, record := range decoded.Records {
		index, err := strconv.Atoi(record.ID)
		if err != nil || index < 0 || index >= len(passages) {
			return nil, fmt.Errorf("the Ranking API returned record %q, which was not sent", record.ID)
		}
		scored = append(scored, Scored{Index: index, Score: record.Score})
	}
	return scored, nil
}
