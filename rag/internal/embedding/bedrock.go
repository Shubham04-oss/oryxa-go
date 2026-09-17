package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// InvokeAPI is the part of Bedrock Runtime this package uses.
type InvokeAPI interface {
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

// NewBedrock builds an embedder over a Bedrock model. The AWS identity comes
// from the process's own role: rag never handles a caller's credentials.
func NewBedrock(client InvokeAPI, spec domain.EmbeddingSpec, options Options) (*Client, error) {
	if client == nil {
		return nil, errors.New("a Bedrock client is required")
	}
	if spec.Provider != domain.ProviderBedrock {
		return nil, fmt.Errorf("%s is not a Bedrock model: %w", spec.Model, domain.ErrInvalid)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return newClient(&bedrockBackend{client: client, spec: spec, family: bedrockFamily(spec.Model)}, spec, options), nil
}

// family is the request shape a Bedrock embedding model speaks. Bedrock hosts
// several vendors behind one InvokeModel call, and each keeps its own body
// format, so the model id is what says which to send.
type family int

const (
	familyTitan family = iota
	familyCohere
)

func bedrockFamily(model string) family {
	if strings.HasPrefix(model, "cohere.") {
		return familyCohere
	}
	return familyTitan
}

type bedrockBackend struct {
	client InvokeAPI
	spec   domain.EmbeddingSpec
	family family
}

func (backend *bedrockBackend) embed(ctx context.Context, kind domain.InputKind, texts []string) (Result, error) {
	body, err := backend.request(kind, texts)
	if err != nil {
		return Result{}, err
	}
	output, err := backend.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(backend.spec.Model),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return Result{}, bedrockError(err)
	}
	return backend.response(output.Body, len(texts))
}

// titanRequest is the body Titan reads. Dimensions and Normalize are only
// understood by v2; v1 emits a fixed 1536-wide, already-normalized vector and
// rejects the fields, so they are omitted for it.
type titanRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions,omitempty"`
	Normalize  *bool  `json:"normalize,omitempty"`
}

type titanResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

// cohereRequest is the body Cohere reads. input_type is the asymmetry: the
// same sentence embeds differently as a document and as a query, and Cohere
// requires the field rather than defaulting it.
type cohereRequest struct {
	Texts          []string `json:"texts"`
	InputType      string   `json:"input_type"`
	Truncate       string   `json:"truncate"`
	EmbeddingTypes []string `json:"embedding_types,omitempty"`
}

func (backend *bedrockBackend) request(kind domain.InputKind, texts []string) ([]byte, error) {
	switch backend.family {
	case familyCohere:
		inputType := "search_document"
		if kind == domain.InputQuery {
			inputType = "search_query"
		}
		return json.Marshal(cohereRequest{
			Texts:     texts,
			InputType: inputType,
			// The chunker already fits the model, so this only catches the
			// pathological case — but END truncation beats a failed batch in
			// the middle of a backfill.
			Truncate:       "END",
			EmbeddingTypes: []string{"float"},
		})
	default:
		if len(texts) != 1 {
			return nil, fmt.Errorf("%s embeds one text per call, got %d", backend.spec.Model, len(texts))
		}
		request := titanRequest{InputText: texts[0]}
		if backend.spec.Model != "amazon.titan-embed-text-v1" {
			request.Dimensions = backend.spec.Dimensions
			request.Normalize = aws.Bool(true)
		}
		return json.Marshal(request)
	}
}

func (backend *bedrockBackend) response(body []byte, want int) (Result, error) {
	switch backend.family {
	case familyCohere:
		return cohereEmbeddings(body)
	default:
		var decoded titanResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return Result{}, fmt.Errorf("decode %s response: %w", backend.spec.Model, err)
		}
		if len(decoded.Embedding) == 0 {
			return Result{}, fmt.Errorf("%s returned no embedding", backend.spec.Model)
		}
		_ = want
		return Result{
			Vectors: [][]float32{decoded.Embedding},
			Usage:   domain.Usage{EmbeddingTokens: decoded.InputTextTokenCount},
		}, nil
	}
}

// cohereEmbeddings reads either response shape Cohere returns.
//
// Asking for embedding_types gets a map keyed by type; without it, or on an
// older deployment, the same field is a bare array. Handling both means a
// region that serves the older shape does not take the service down.
func cohereEmbeddings(body []byte) (Result, error) {
	var envelope struct {
		Embeddings json.RawMessage `json:"embeddings"`
		Meta       struct {
			BilledUnits struct {
				InputTokens int `json:"input_tokens"`
			} `json:"billed_units"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Result{}, fmt.Errorf("decode Cohere response: %w", err)
	}
	var vectors [][]float32
	if err := json.Unmarshal(envelope.Embeddings, &vectors); err != nil {
		var byType struct {
			Float [][]float32 `json:"float"`
		}
		if err := json.Unmarshal(envelope.Embeddings, &byType); err != nil {
			return Result{}, fmt.Errorf("decode Cohere embeddings: %w", err)
		}
		vectors = byType.Float
	}
	if len(vectors) == 0 {
		return Result{}, errors.New("Cohere returned no embeddings")
	}
	return Result{Vectors: vectors, Usage: domain.Usage{EmbeddingTokens: envelope.Meta.BilledUnits.InputTokens}}, nil
}

// bedrockError marks the failures worth retrying.
//
// Throttling is the ordinary case on a backfill and must not end the run. A
// model that is still warming up, a timeout, or a 5xx are all worth another
// attempt. Validation and access errors are not: retrying those just spends
// the quota on the same rejection.
func bedrockError(err error) error {
	var (
		throttling  *bedrocktypes.ThrottlingException
		quota       *bedrocktypes.ServiceQuotaExceededException
		notReady    *bedrocktypes.ModelNotReadyException
		timeout     *bedrocktypes.ModelTimeoutException
		internal    *bedrocktypes.InternalServerException
		unavailable *bedrocktypes.ServiceUnavailableException
	)
	switch {
	case errors.As(err, &throttling), errors.As(err, &quota), errors.As(err, &notReady):
		return fmt.Errorf("%w: %w", ErrThrottled, err)
	case errors.As(err, &timeout), errors.As(err, &internal), errors.As(err, &unavailable):
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	return err
}
