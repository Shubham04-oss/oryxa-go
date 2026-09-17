package generation

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

// VertexScope is the OAuth scope a Vertex AI generation needs.
const VertexScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexConfig says where Vertex AI serves the model. It matches how the
// agents service is configured, so one project and location serve both.
type VertexConfig struct {
	Project  string
	Location string
	Model    string
	// HTTPClient carries the credentials. Nil means application default
	// credentials, which is what the deployed service uses; tests set it.
	HTTPClient *http.Client
	// Endpoint overrides the regional host, for tests and private endpoints.
	Endpoint string
}

// Vertex answers with a Gemini model on Vertex AI.
type Vertex struct {
	client   *http.Client
	model    string
	endpoint string
}

func NewVertex(ctx context.Context, config VertexConfig) (*Vertex, error) {
	if strings.TrimSpace(config.Project) == "" {
		return nil, errors.New("a Google Cloud project is required: Vertex AI serves the model")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("a Vertex model is required to answer questions")
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
		// Generating a long grounded answer is slower than embedding one.
		client.Timeout = 120 * time.Second
	}
	host := config.Endpoint
	if host == "" {
		if config.Location == "global" {
			host = "https://aiplatform.googleapis.com"
		} else {
			host = "https://" + config.Location + "-aiplatform.googleapis.com"
		}
	}
	return &Vertex{
		client: client,
		model:  config.Model,
		endpoint: fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
			strings.TrimRight(host, "/"), url.PathEscape(config.Project),
			url.PathEscape(config.Location), url.PathEscape(config.Model)),
	}, nil
}

func (generator *Vertex) Model() string { return generator.model }

type vertexPart struct {
	Text string `json:"text"`
}

type vertexContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []vertexPart `json:"parts"`
}

type vertexRequest struct {
	Contents          []vertexContent `json:"contents"`
	SystemInstruction *vertexContent  `json:"systemInstruction,omitempty"`
	GenerationConfig  struct {
		MaxOutputTokens int     `json:"maxOutputTokens"`
		Temperature     float32 `json:"temperature"`
	} `json:"generationConfig"`
}

type vertexResponse struct {
	Candidates []struct {
		Content      vertexContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (generator *Vertex) Generate(ctx context.Context, request Request) (Response, error) {
	system, user := Prompt(request)
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxAnswerTokens
	}
	body := vertexRequest{
		Contents:          []vertexContent{{Role: "user", Parts: []vertexPart{{Text: user}}}},
		SystemInstruction: &vertexContent{Parts: []vertexPart{{Text: system}}},
	}
	body.GenerationConfig.MaxOutputTokens = maxTokens
	// Same reasoning as on Bedrock: the same sources should give the same
	// answer, and sampling makes a wrong one harder to reproduce.
	body.GenerationConfig.Temperature = 0

	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode Vertex request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, generator.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Response{}, fmt.Errorf("build Vertex request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	httpResponse, err := generator.client.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("call Vertex AI: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, 1<<16))
		_ = httpResponse.Body.Close()
	}()
	if httpResponse.StatusCode != http.StatusOK {
		return Response{}, vertexError(httpResponse)
	}
	var decoded vertexResponse
	if err := json.NewDecoder(io.LimitReader(httpResponse.Body, 8<<20)).Decode(&decoded); err != nil {
		return Response{}, fmt.Errorf("decode Vertex response: %w", err)
	}
	if len(decoded.Candidates) == 0 {
		return Response{}, fmt.Errorf("%s returned no candidates", generator.model)
	}
	var answer strings.Builder
	for _, part := range decoded.Candidates[0].Content.Parts {
		answer.WriteString(part.Text)
	}
	if answer.Len() == 0 {
		// A safety block returns a candidate with no parts. Saying so beats
		// an empty answer that reads as though the documents held nothing.
		return Response{}, fmt.Errorf("%s returned no answer text (finish reason %q)",
			generator.model, decoded.Candidates[0].FinishReason)
	}
	return Response{
		Text: answer.String(),
		Usage: domain.Usage{
			InputTokens:  decoded.UsageMetadata.PromptTokenCount,
			OutputTokens: decoded.UsageMetadata.CandidatesTokenCount,
		},
	}, nil
}

// vertexError reports a failed call without echoing the body, which can quote
// the prompt — and the prompt here is an organization's own document text.
func vertexError(response *http.Response) error {
	var decoded struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&decoded)
	status := decoded.Error.Status
	if status == "" {
		status = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("Vertex AI returned %d %s", response.StatusCode, status)
}
