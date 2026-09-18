package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func titanSpec() domain.EmbeddingSpec {
	return domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 4, Metric: domain.MetricCosine}
}

func cohereSpec() domain.EmbeddingSpec {
	return domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "cohere.embed-english-v3", Dimensions: 4, Metric: domain.MetricCosine}
}

func vertexSpec() domain.EmbeddingSpec {
	return domain.EmbeddingSpec{Provider: domain.ProviderVertex, Model: "text-embedding-005", Dimensions: 4, Metric: domain.MetricCosine}
}

// fakeBedrock records what it was asked and answers with whatever the test
// set up, so the request body and the call count are both observable.
type fakeBedrock struct {
	mutex    sync.Mutex
	requests [][]byte
	respond  func(call int, body []byte) ([]byte, error)
}

func (fake *fakeBedrock) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	fake.mutex.Lock()
	call := len(fake.requests)
	body := append([]byte(nil), input.Body...)
	fake.requests = append(fake.requests, body)
	fake.mutex.Unlock()

	response, err := fake.respond(call, body)
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.InvokeModelOutput{Body: response}, nil
}

func (fake *fakeBedrock) calls() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return len(fake.requests)
}

// unitVector returns a already-normalized vector whose first element encodes
// seed, so a test can tell the vectors apart and check their order.
func unitVector(seed int, width int) []float32 {
	values := make([]float32, width)
	values[0] = 1
	if width > 1 {
		values[1] = float32(seed)
	}
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	length := float32(math.Sqrt(sum))
	for index := range values {
		values[index] /= length
	}
	return values
}

func titanBody(t *testing.T, seed int) []byte {
	t.Helper()
	body, err := json.Marshal(titanResponse{Embedding: unitVector(seed, 4), InputTextTokenCount: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// noSleep removes the backoff wait so a retry test runs instantly.
func noSleep(client *Client) *Client {
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestBedrockTitanEmbedsOneTextPerCallInOrder(t *testing.T) {
	t.Parallel()
	texts := []string{"first", "second", "third"}
	seeds := map[string]int{"first": 1, "second": 2, "third": 3}
	// The response is keyed to the text that asked for it, not to arrival
	// order: batches run concurrently, so arrival order says nothing. This is
	// what makes the assertion below a real test of ordering.
	fake := &fakeBedrock{respond: func(_ int, body []byte) ([]byte, error) {
		var request titanRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, err
		}
		seed, known := seeds[request.InputText]
		if !known {
			return nil, fmt.Errorf("unexpected text %q", request.InputText)
		}
		return titanBody(t, seed), nil
	}}
	client, err := NewBedrock(fake, titanSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	result, err := client.Embed(context.Background(), domain.InputDocument, texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Titan takes one text per invocation; the catalog says so and the client
	// must have split accordingly rather than sending an array.
	if fake.calls() != 3 {
		t.Fatalf("made %d calls, want one per text", fake.calls())
	}
	if len(result.Vectors) != 3 {
		t.Fatalf("got %d vectors, want 3", len(result.Vectors))
	}
	for index, vector := range result.Vectors {
		if want := unitVector(index+1, 4)[1]; vector[1] != want {
			t.Fatalf("vector %d is %v, want the one seeded %d — order was not preserved", index, vector, index+1)
		}
	}
	if result.Usage.EmbeddedTexts != 3 || result.Usage.EmbeddingTokens != 21 {
		t.Fatalf("usage = %+v, want 3 texts and 21 tokens", result.Usage)
	}
}

func TestBedrockTitanRequestShape(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(call int, _ []byte) ([]byte, error) { return titanBody(t, call), nil }}
	client, err := NewBedrock(fake, titanSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := client.Embed(context.Background(), domain.InputDocument, []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	var sent titanRequest
	if err := json.Unmarshal(fake.requests[0], &sent); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if sent.InputText != "hello" || sent.Dimensions != 4 || sent.Normalize == nil || !*sent.Normalize {
		t.Fatalf("request = %+v", sent)
	}
}

func TestBedrockTitanV1OmitsFieldsItRejects(t *testing.T) {
	t.Parallel()
	spec := domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v1", Dimensions: 4, Metric: domain.MetricCosine}
	fake := &fakeBedrock{respond: func(call int, _ []byte) ([]byte, error) { return titanBody(t, call), nil }}
	client, err := NewBedrock(fake, spec, Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := client.Embed(context.Background(), domain.InputDocument, []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	body := string(fake.requests[0])
	if strings.Contains(body, "dimensions") || strings.Contains(body, "normalize") {
		t.Fatalf("v1 request carried fields it rejects: %s", body)
	}
}

func TestBedrockCohereBatchesAndSetsInputType(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		kind domain.InputKind
		want string
	}{
		{domain.InputDocument, "search_document"},
		{domain.InputQuery, "search_query"},
	} {
		t.Run(string(testCase.kind), func(t *testing.T) {
			t.Parallel()
			fake := &fakeBedrock{respond: func(_ int, body []byte) ([]byte, error) {
				var request cohereRequest
				if err := json.Unmarshal(body, &request); err != nil {
					return nil, err
				}
				vectors := make([][]float32, len(request.Texts))
				for index := range vectors {
					vectors[index] = unitVector(index+1, 4)
				}
				return json.Marshal(map[string]any{
					"embeddings": map[string]any{"float": vectors},
					"meta":       map[string]any{"billed_units": map[string]int{"input_tokens": len(request.Texts)}},
				})
			}}
			client, err := NewBedrock(fake, cohereSpec(), Options{})
			if err != nil {
				t.Fatalf("NewBedrock: %v", err)
			}
			texts := make([]string, 5)
			for index := range texts {
				texts[index] = fmt.Sprintf("text %d", index)
			}
			result, err := client.Embed(context.Background(), testCase.kind, texts)
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}
			// Cohere takes ninety-six per call, so five texts are one call.
			if fake.calls() != 1 {
				t.Fatalf("made %d calls, want 1", fake.calls())
			}
			if len(result.Vectors) != 5 {
				t.Fatalf("got %d vectors, want 5", len(result.Vectors))
			}
			var sent cohereRequest
			if err := json.Unmarshal(fake.requests[0], &sent); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if sent.InputType != testCase.want {
				t.Fatalf("input_type = %q, want %q", sent.InputType, testCase.want)
			}
		})
	}
}

func TestBedrockCohereReadsTheBareArrayShape(t *testing.T) {
	t.Parallel()
	// Older deployments return embeddings as an array rather than keyed by
	// type. Both have to work or a region upgrade takes the service down.
	fake := &fakeBedrock{respond: func(_ int, _ []byte) ([]byte, error) {
		return json.Marshal(map[string]any{"embeddings": [][]float32{unitVector(1, 4), unitVector(2, 4)}})
	}}
	client, err := NewBedrock(fake, cohereSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	result, err := client.Embed(context.Background(), domain.InputDocument, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(result.Vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(result.Vectors))
	}
}

func TestBedrockRetriesThrottlingThenSucceeds(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(call int, _ []byte) ([]byte, error) {
		if call < 2 {
			return nil, &bedrocktypes.ThrottlingException{Message: stringPointer("slow down")}
		}
		return titanBody(t, 1), nil
	}}
	client, err := NewBedrock(fake, titanSpec(), Options{MaxAttempts: 4})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := noSleep(client).Embed(context.Background(), domain.InputDocument, []string{"hello"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if fake.calls() != 3 {
		t.Fatalf("made %d calls, want 2 throttled then 1 that worked", fake.calls())
	}
}

func TestBedrockGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(int, []byte) ([]byte, error) {
		return nil, &bedrocktypes.ThrottlingException{Message: stringPointer("slow down")}
	}}
	client, err := NewBedrock(fake, titanSpec(), Options{MaxAttempts: 3})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := noSleep(client).Embed(context.Background(), domain.InputDocument, []string{"hello"}); err == nil {
		t.Fatal("Embed succeeded after every attempt was throttled")
	}
	if fake.calls() != 3 {
		t.Fatalf("made %d calls, want exactly MaxAttempts of 3", fake.calls())
	}
}

func TestBedrockDoesNotRetryWhatWillNotChange(t *testing.T) {
	t.Parallel()
	// A validation error will fail identically every time; retrying it just
	// spends the quota on the same rejection.
	fake := &fakeBedrock{respond: func(int, []byte) ([]byte, error) {
		return nil, &bedrocktypes.ValidationException{Message: stringPointer("bad input")}
	}}
	client, err := NewBedrock(fake, titanSpec(), Options{MaxAttempts: 5})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := noSleep(client).Embed(context.Background(), domain.InputDocument, []string{"hello"}); err == nil {
		t.Fatal("Embed succeeded on a validation error")
	}
	if fake.calls() != 1 {
		t.Fatalf("made %d calls, want 1", fake.calls())
	}
}

func TestEmbedRefusesAVectorOfTheWrongWidth(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(int, []byte) ([]byte, error) {
		return json.Marshal(titanResponse{Embedding: make([]float32, 8)})
	}}
	client, err := NewBedrock(fake, titanSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	_, err = client.Embed(context.Background(), domain.InputDocument, []string{"hello"})
	if err == nil {
		t.Fatal("an 8-wide vector was accepted into a 4-wide corpus")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestEmbedRefusesAnEmptyTextAndAnUnknownKind(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(call int, _ []byte) ([]byte, error) { return titanBody(t, call), nil }}
	client, err := NewBedrock(fake, titanSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	if _, err := client.Embed(context.Background(), domain.InputDocument, []string{"ok", ""}); err == nil {
		t.Fatal("an empty text was accepted")
	}
	if _, err := client.Embed(context.Background(), domain.InputKind("both"), []string{"ok"}); err == nil {
		t.Fatal("an unknown input kind was accepted")
	}
	if fake.calls() != 0 {
		t.Fatalf("made %d provider calls for input that should never have left: %d", fake.calls(), fake.calls())
	}
}

func TestEmbedNothingIsNotAnError(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(int, []byte) ([]byte, error) { return nil, errors.New("must not be called") }}
	client, err := NewBedrock(fake, titanSpec(), Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	result, err := client.Embed(context.Background(), domain.InputDocument, nil)
	if err != nil || len(result.Vectors) != 0 {
		t.Fatalf("Embed(nil) = (%v, %v)", result, err)
	}
}

func TestEmbedNormalizesAModelTheCatalogDoesNotVouchFor(t *testing.T) {
	t.Parallel()
	spec := domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "vendor.unlisted-embed-v1", Dimensions: 4, Metric: domain.MetricCosine}
	fake := &fakeBedrock{respond: func(int, []byte) ([]byte, error) {
		return json.Marshal(titanResponse{Embedding: []float32{3, 4, 0, 0}})
	}}
	client, err := NewBedrock(fake, spec, Options{})
	if err != nil {
		t.Fatalf("NewBedrock: %v", err)
	}
	result, err := client.Embed(context.Background(), domain.InputDocument, []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	var sum float64
	for _, value := range result.Vectors[0] {
		sum += float64(value) * float64(value)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-6 {
		t.Fatalf("vector length is %v, want 1: an unvouched model must be normalized", math.Sqrt(sum))
	}
}

func TestVertexRequestAndResponse(t *testing.T) {
	t.Parallel()
	var (
		gotPath string
		gotBody vertexRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		response := vertexResponse{}
		for index := range gotBody.Instances {
			var prediction struct {
				Embeddings struct {
					Values     []float32 `json:"values"`
					Statistics struct {
						TokenCount int  `json:"token_count"`
						Truncated  bool `json:"truncated"`
					} `json:"statistics"`
				} `json:"embeddings"`
			}
			prediction.Embeddings.Values = unitVector(index+1, 4)
			prediction.Embeddings.Statistics.TokenCount = 5
			response.Predictions = append(response.Predictions, prediction)
		}
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	client, err := NewVertex(context.Background(), vertexSpec(), VertexConfig{
		Project: "oryxa-dev", Location: "us-central1", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	result, err := client.Embed(context.Background(), domain.InputQuery, []string{"how do credentials rotate?"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if want := "/v1/projects/oryxa-dev/locations/us-central1/publishers/google/models/text-embedding-005:predict"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotBody.Instances[0].TaskType != "RETRIEVAL_QUERY" {
		t.Fatalf("task_type = %q, want RETRIEVAL_QUERY", gotBody.Instances[0].TaskType)
	}
	if gotBody.Parameters.OutputDimensionality != 4 || !gotBody.Parameters.AutoTruncate {
		t.Fatalf("parameters = %+v", gotBody.Parameters)
	}
	if len(result.Vectors) != 1 || result.Usage.EmbeddingTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
}

func TestVertexDocumentsUseTheDocumentTaskType(t *testing.T) {
	t.Parallel()
	var gotBody vertexRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewDecoder(request.Body).Decode(&gotBody)
		_, _ = writer.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,0,0,0]}}]}`))
	}))
	defer server.Close()
	client, err := NewVertex(context.Background(), vertexSpec(), VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if _, err := client.Embed(context.Background(), domain.InputDocument, []string{"a passage"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotBody.Instances[0].TaskType != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("task_type = %q, want RETRIEVAL_DOCUMENT", gotBody.Instances[0].TaskType)
	}
}

func TestVertexRetriesTooManyRequests(t *testing.T) {
	t.Parallel()
	var calls int
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		calls++
		attempt := calls
		mutex.Unlock()
		if attempt < 3 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,0,0,0]}}]}`))
	}))
	defer server.Close()
	client, err := NewVertex(context.Background(), vertexSpec(), VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{MaxAttempts: 5})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if _, err := noSleep(client).Embed(context.Background(), domain.InputDocument, []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if calls != 3 {
		t.Fatalf("made %d calls, want 2 throttled then 1 that worked", calls)
	}
}

func TestVertexDoesNotRetryABadRequest(t *testing.T) {
	t.Parallel()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"the document said something private"}}`))
	}))
	defer server.Close()
	client, err := NewVertex(context.Background(), vertexSpec(), VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{MaxAttempts: 5})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	_, err = noSleep(client).Embed(context.Background(), domain.InputDocument, []string{"a"})
	if err == nil {
		t.Fatal("a 400 was retried into success")
	}
	if calls != 1 {
		t.Fatalf("made %d calls, want 1", calls)
	}
	// A provider error may quote the input, which here is an organization's
	// own text. It must not end up in a log line or an API response.
	if strings.Contains(err.Error(), "said something private") {
		t.Fatalf("the error echoed the provider's message body: %v", err)
	}
}

func TestVertexEndpointPerLocation(t *testing.T) {
	t.Parallel()
	global := vertexEndpoint(VertexConfig{Project: "p", Location: "global"}, "gemini-embedding-001")
	if !strings.HasPrefix(global, "https://aiplatform.googleapis.com/") {
		t.Fatalf("global endpoint = %q", global)
	}
	regional := vertexEndpoint(VertexConfig{Project: "p", Location: "asia-south1"}, "text-embedding-005")
	if !strings.HasPrefix(regional, "https://asia-south1-aiplatform.googleapis.com/") {
		t.Fatalf("regional endpoint = %q", regional)
	}
}

func TestVertexBatchesToWhatTheModelAccepts(t *testing.T) {
	t.Parallel()
	// gemini-embedding-001 takes one instance per request. Sending more is
	// rejected by Vertex, so the catalog's limit has to be honoured.
	var mutex sync.Mutex
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body vertexRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		mutex.Lock()
		sizes = append(sizes, len(body.Instances))
		mutex.Unlock()
		_, _ = writer.Write([]byte(`{"predictions":[{"embeddings":{"values":[1,0,0,0]}}]}`))
	}))
	defer server.Close()
	spec := domain.EmbeddingSpec{Provider: domain.ProviderVertex, Model: "gemini-embedding-001", Dimensions: 4, Metric: domain.MetricCosine}
	client, err := NewVertex(context.Background(), spec, VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if _, err := client.Embed(context.Background(), domain.InputDocument, []string{"a", "b", "c"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(sizes) != 3 {
		t.Fatalf("made %d calls, want one per text", len(sizes))
	}
	for _, size := range sizes {
		if size != 1 {
			t.Fatalf("a call carried %d instances, want 1", size)
		}
	}
}

// TestVertexBatchesStayUnderTheRequestTokenLimit is the regression for a
// document that could never be indexed: text-embedding-005 takes 250 texts a
// call but refuses a call of more than 20,000 tokens, so a batch sized by count
// alone failed on every attempt once a document's chunks were long enough.
func TestVertexBatchesStayUnderTheRequestTokenLimit(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	var requestTokens []int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body vertexRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		tokens := 0
		predictions := make([]string, 0, len(body.Instances))
		for _, instance := range body.Instances {
			tokens += chunkingEstimate(instance.Content)
			predictions = append(predictions, `{"embeddings":{"values":[1,0,0,0]}}`)
		}
		mutex.Lock()
		requestTokens = append(requestTokens, tokens)
		mutex.Unlock()
		_, _ = writer.Write([]byte(`{"predictions":[` + strings.Join(predictions, ",") + `]}`))
	}))
	defer server.Close()
	spec := domain.EmbeddingSpec{Provider: domain.ProviderVertex, Model: "text-embedding-005", Dimensions: 4, Metric: domain.MetricCosine}
	client, err := NewVertex(context.Background(), spec, VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	// 120 chunks of about 600 tokens: far under 250 texts, far over 20,000
	// tokens in one call.
	texts := make([]string, 120)
	for index := range texts {
		texts[index] = fmt.Sprintf("chunk %d ", index) + strings.Repeat("retrieval keeps passages whole ", 80)
	}
	result, err := client.Embed(context.Background(), domain.InputDocument, texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(result.Vectors) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(result.Vectors), len(texts))
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(requestTokens) < 4 {
		t.Fatalf("made %d calls for about 72,000 tokens, want at least 4", len(requestTokens))
	}
	for _, tokens := range requestTokens {
		if tokens > 20000 {
			t.Fatalf("a call carried about %d tokens, over Vertex's 20,000", tokens)
		}
	}
}

// TestVertexHalvesABatchItRefusesAsTooLarge: a batch is sized by an estimate,
// and code or a language the model's vocabulary serves less well can run
// denser than it expects. Vertex's refusal names the token limit, and the
// batch is embedded in halves instead, in order.
func TestVertexHalvesABatchItRefusesAsTooLarge(t *testing.T) {
	t.Parallel()
	var mutex sync.Mutex
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body vertexRequest
		_ = json.NewDecoder(request.Body).Decode(&body)
		mutex.Lock()
		sizes = append(sizes, len(body.Instances))
		mutex.Unlock()
		if len(body.Instances) > 8 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Unable to submit request because the input token count is 23563 but the model supports up to 20000."}}`))
			return
		}
		predictions := make([]string, 0, len(body.Instances))
		for _, instance := range body.Instances {
			var index int
			_, _ = fmt.Sscanf(instance.Content, "t%d", &index)
			predictions = append(predictions, fmt.Sprintf(`{"embeddings":{"values":[%d,0,0,1]}}`, index))
		}
		_, _ = writer.Write([]byte(`{"predictions":[` + strings.Join(predictions, ",") + `]}`))
	}))
	defer server.Close()
	spec := domain.EmbeddingSpec{Provider: domain.ProviderVertex, Model: "text-embedding-005", Dimensions: 4, Metric: domain.MetricCosine}
	client, err := NewVertex(context.Background(), spec, VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	texts := make([]string, 40)
	for index := range texts {
		texts[index] = fmt.Sprintf("t%d", index)
	}
	result, err := noSleep(client).Embed(context.Background(), domain.InputDocument, texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for index, vector := range result.Vectors {
		if int(vector[0]) != index {
			t.Fatalf("vector %d belongs to text %d: halving lost the order", index, int(vector[0]))
		}
	}
	if len(result.Vectors) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(result.Vectors), len(texts))
	}

	// A single text refused as too large has nowhere smaller to go.
	one, err := NewVertex(context.Background(), spec, VertexConfig{
		Project: "p", Location: "global", HTTPClient: server.Client(), Endpoint: server.URL,
	}, Options{MaxBatch: 1})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"status":"INVALID_ARGUMENT","message":"the input token count is 30000 but the model supports up to 20000"}}`))
	})
	if _, err := noSleep(one).Embed(context.Background(), domain.InputDocument, []string{"t0"}); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("Embed of one refused text = %v, want ErrBatchTooLarge", err)
	}
}

func TestTruncateRespectsTheModelLimit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 1000)
	cut, wasCut := truncate(long, 64)
	if !wasCut {
		t.Fatal("a long text was not truncated")
	}
	if got := chunkingEstimate(cut); got > 64 {
		t.Fatalf("truncated text is %d tokens, over the 64 limit", got)
	}
	if same, wasCut := truncate("short", 64); wasCut || same != "short" {
		t.Fatalf("truncate(%q) = (%q, %v)", "short", same, wasCut)
	}
	if same, wasCut := truncate("anything", 0); wasCut || same != "anything" {
		t.Fatal("truncate applied a limit of zero")
	}
}

func TestRegistryReusesAnEmbedderPerSpec(t *testing.T) {
	t.Parallel()
	fake := &fakeBedrock{respond: func(call int, _ []byte) ([]byte, error) { return titanBody(t, call), nil }}
	registry := NewRegistry(Config{Bedrock: fake})
	first, err := registry.For(context.Background(), titanSpec())
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	again, err := registry.For(context.Background(), titanSpec())
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if first != again {
		t.Fatal("the registry built a second embedder for the same spec")
	}
	other, err := registry.For(context.Background(), cohereSpec())
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if other == first {
		t.Fatal("two different specs share one embedder")
	}
}

func TestRegistrySaysWhichProviderIsMissing(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(Config{Bedrock: &fakeBedrock{}})
	_, err := registry.For(context.Background(), vertexSpec())
	if err == nil {
		t.Fatal("a Vertex spec was served with no Google Cloud project configured")
	}
	if !strings.Contains(err.Error(), "Google Cloud project") {
		t.Fatalf("error = %v, want it to name what is missing", err)
	}
	if providers := registry.Providers(); len(providers) != 1 || providers[0] != domain.ProviderBedrock {
		t.Fatalf("Providers = %v", providers)
	}
}

func TestBackoffStaysWithinTheCeiling(t *testing.T) {
	t.Parallel()
	client := newClient(nil, titanSpec(), Options{BaseDelay: time.Second, MaxDelay: 4 * time.Second})
	for attempt := 1; attempt <= 12; attempt++ {
		delay := client.backoff(attempt)
		if delay <= 0 || delay > client.options.MaxDelay {
			t.Fatalf("attempt %d gave a delay of %v, outside (0, %v]", attempt, delay, client.options.MaxDelay)
		}
	}
}

func stringPointer(value string) *string { return &value }

// chunkingEstimate is the same estimator the chunker uses, re-exported here so
// a truncation test asserts against the number the limit is enforced with.
func chunkingEstimate(text string) int { return chunking.EstimateTokens(text) }
