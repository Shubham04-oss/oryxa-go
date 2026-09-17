package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// VertexScope is the OAuth scope a Vertex AI prediction needs.
const VertexScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexConfig says where Vertex AI serves the model. It matches how the
// agents service is configured, so one project and location serve both.
type VertexConfig struct {
	Project  string
	Location string
	// HTTPClient carries the credentials. Leave it nil and application
	// default credentials are used, which is what the deployed service does;
	// tests set it to reach a stub.
	HTTPClient *http.Client
	// Endpoint overrides the regional host. It is only for tests and for a
	// private service endpoint.
	Endpoint string
}

// NewVertex builds an embedder over a Vertex AI publisher model.
func NewVertex(ctx context.Context, spec domain.EmbeddingSpec, config VertexConfig, options Options) (*Client, error) {
	if spec.Provider != domain.ProviderVertex {
		return nil, fmt.Errorf("%s is not a Vertex model: %w", spec.Model, domain.ErrInvalid)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Project) == "" {
		return nil, errors.New("a Google Cloud project is required: Vertex AI serves the embedding models")
	}
	if strings.TrimSpace(config.Location) == "" {
		config.Location = "global"
	}
	client := config.HTTPClient
	if client == nil {
		source, err := google.DefaultTokenSource(ctx, VertexScope)
		if err != nil {
			return nil, fmt.Errorf("find Google application default credentials: %w", err)
		}
		client = oauth2.NewClient(ctx, source)
		client.Timeout = 60 * time.Second
	}
	return newClient(&vertexBackend{
		client:   client,
		spec:     spec,
		endpoint: vertexEndpoint(config, spec.Model),
	}, spec, options), nil
}

// vertexEndpoint builds the :predict URL. The global location is served from
// the unprefixed host; every other location has its own.
func vertexEndpoint(config VertexConfig, model string) string {
	host := config.Endpoint
	if host == "" {
		if config.Location == "global" {
			host = "https://aiplatform.googleapis.com"
		} else {
			host = "https://" + config.Location + "-aiplatform.googleapis.com"
		}
	}
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		strings.TrimRight(host, "/"), url.PathEscape(config.Project), url.PathEscape(config.Location), url.PathEscape(model))
}

type vertexBackend struct {
	client   *http.Client
	spec     domain.EmbeddingSpec
	endpoint string
}

// vertexInstance is one text to embed. task_type is the asymmetry: Vertex
// places a passage and a question about that passage differently on purpose,
// and defaulting it would throw that away.
type vertexInstance struct {
	Content  string `json:"content"`
	TaskType string `json:"task_type"`
}

type vertexParameters struct {
	OutputDimensionality int  `json:"outputDimensionality,omitempty"`
	AutoTruncate         bool `json:"autoTruncate"`
}

type vertexRequest struct {
	Instances  []vertexInstance `json:"instances"`
	Parameters vertexParameters `json:"parameters"`
}

type vertexResponse struct {
	Predictions []struct {
		Embeddings struct {
			Values     []float32 `json:"values"`
			Statistics struct {
				TokenCount int  `json:"token_count"`
				Truncated  bool `json:"truncated"`
			} `json:"statistics"`
		} `json:"embeddings"`
	} `json:"predictions"`
}

func (backend *vertexBackend) embed(ctx context.Context, kind domain.InputKind, texts []string) (Result, error) {
	taskType := "RETRIEVAL_DOCUMENT"
	if kind == domain.InputQuery {
		taskType = "RETRIEVAL_QUERY"
	}
	instances := make([]vertexInstance, 0, len(texts))
	for _, text := range texts {
		instances = append(instances, vertexInstance{Content: text, TaskType: taskType})
	}
	// outputDimensionality is only meaningful where the model was trained to
	// be truncated; sending the model's own default width is harmless.
	body, err := json.Marshal(vertexRequest{
		Instances:  instances,
		Parameters: vertexParameters{OutputDimensionality: backend.spec.Dimensions, AutoTruncate: true},
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode Vertex request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("build Vertex request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := backend.client.Do(request)
	if err != nil {
		// A connection that failed on the way out is worth another attempt.
		return Result{}, fmt.Errorf("%w: call Vertex AI: %w", ErrTransient, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return Result{}, vertexError(response)
	}
	var decoded vertexResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&decoded); err != nil {
		return Result{}, fmt.Errorf("decode Vertex response: %w", err)
	}
	result := Result{Vectors: make([][]float32, 0, len(decoded.Predictions))}
	for _, prediction := range decoded.Predictions {
		if len(prediction.Embeddings.Values) == 0 {
			return Result{}, errors.New("Vertex AI returned a prediction with no values")
		}
		result.Vectors = append(result.Vectors, prediction.Embeddings.Values)
		result.Usage.EmbeddingTokens += prediction.Embeddings.Statistics.TokenCount
	}
	return result, nil
}

// vertexError turns a failed response into an error that says whether it is
// worth retrying. The body is read but never echoed whole: a Vertex error can
// quote the input, which here is an organization's document text.
func vertexError(response *http.Response) error {
	var decoded struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&decoded)
	status := decoded.Error.Status
	if status == "" {
		status = http.StatusText(response.StatusCode)
	}
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: Vertex AI returned %s", ErrThrottled, status)
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: Vertex AI returned %s", ErrTransient, status)
	default:
		return fmt.Errorf("Vertex AI returned %d %s", response.StatusCode, status)
	}
}
