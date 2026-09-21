package rig

import (
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// FakeBedrock is a stand-in for Bedrock Runtime's InvokeModel, enough for rag
// to embed text with a Titan model. Embeddings are a hashed bag of words, so
// texts that share words are near each other and a search returns something
// sensible without any model behind it.
type FakeBedrock struct {
	mu       sync.Mutex
	mode     string
	requests atomic.Int64
	inputs   atomic.Int64 // bytes of text it was asked to embed
}

// NewFakeBedrock returns a healthy fake.
func NewFakeBedrock() *FakeBedrock { return &FakeBedrock{} }

// SetMode picks how the fake answers: "" (well), "wrongdim", "nan", "empty",
// "500", "throttle", "slow", "garbage", "huge".
func (fake *FakeBedrock) SetMode(mode string) {
	fake.mu.Lock()
	fake.mode = mode
	fake.mu.Unlock()
}

// Requests is how many embeddings were asked for.
func (fake *FakeBedrock) Requests() int64 { return fake.requests.Load() }

// ServeHTTP implements http.Handler.
func (fake *FakeBedrock) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.requests.Add(1)
	fake.mu.Lock()
	mode := fake.mode
	fake.mu.Unlock()

	if !strings.HasSuffix(request.URL.Path, "/invoke") || request.Method != http.MethodPost {
		writer.Header().Set("x-amzn-errortype", "ValidationException")
		writeJSON(writer, http.StatusBadRequest, map[string]string{"message": "unsupported operation " + request.URL.Path})
		return
	}
	var body struct {
		InputText  string `json:"inputText"`
		Dimensions int    `json:"dimensions"`
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 16<<20)).Decode(&body); err != nil {
		writer.Header().Set("x-amzn-errortype", "ValidationException")
		writeJSON(writer, http.StatusBadRequest, map[string]string{"message": "invalid body"})
		return
	}
	fake.inputs.Add(int64(len(body.InputText)))
	dimensions := body.Dimensions
	if dimensions <= 0 {
		dimensions = 1024
	}

	switch mode {
	case "500":
		writer.Header().Set("x-amzn-errortype", "InternalServerException")
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"message": "injected"})
		return
	case "throttle":
		writer.Header().Set("x-amzn-errortype", "ThrottlingException")
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{"message": "Too many requests"})
		return
	case "garbage":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"embedding":[0.1,0.2,`))
		return
	case "empty":
		writeJSON(writer, http.StatusOK, map[string]any{"embedding": []float32{}, "inputTextTokenCount": 0})
		return
	case "slow":
		select {
		case <-time.After(15 * time.Second):
		case <-request.Context().Done():
			return
		}
	case "wrongdim":
		dimensions /= 2
	case "nan":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"embedding":[NaN,Infinity,-Infinity],"inputTextTokenCount":3}`))
		return
	case "huge":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"embedding":[`))
		for range 8 << 20 {
			if _, err := writer.Write([]byte("0.5,")); err != nil {
				return
			}
		}
		_, _ = writer.Write([]byte(`0.5],"inputTextTokenCount":1}`))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"embedding": hashEmbedding(body.InputText, dimensions), "inputTextTokenCount": len(strings.Fields(body.InputText)),
	})
}

// hashEmbedding is a unit vector built by hashing each word into a bucket.
func hashEmbedding(text string, dimensions int) []float32 {
	vector := make([]float64, dimensions)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if len(words) == 0 {
		vector[0] = 1
	}
	for _, word := range words {
		hasher := fnv.New64a()
		_, _ = hasher.Write([]byte(word))
		sum := hasher.Sum64()
		sign := 1.0
		if sum&1 == 1 {
			sign = -1
		}
		vector[int(sum>>1)%dimensions] += sign
	}
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	norm = math.Sqrt(norm)
	out := make([]float32, dimensions)
	for index, value := range vector {
		if norm > 0 {
			out[index] = float32(value / norm)
		}
	}
	return out
}
