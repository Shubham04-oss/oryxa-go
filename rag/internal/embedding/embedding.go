// Package embedding turns text into vectors through Amazon Bedrock or Google
// Vertex AI, behind one interface that the rest of the service uses.
//
// Three things live here rather than in the callers. Every call says whether
// it is embedding a document or a query, because these models are trained
// asymmetrically and using the document form for a query is the quietest way
// to make retrieval mediocre. Batching follows what each model actually
// accepts — Titan takes one text per call, Cohere takes ninety-six, Vertex's
// gemini-embedding-001 takes one — so a limit is read from the catalog rather
// than discovered in production. And throttling is retried with backoff,
// because an embedding run over a large corpus will be throttled; that is
// normal, not exceptional.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

var (
	// ErrThrottled means the provider asked us to slow down. It is retried.
	ErrThrottled = errors.New("embedding provider throttled the request")
	// ErrTransient means the call failed for a reason that may not recur,
	// such as a timeout or a 5xx. It is retried.
	ErrTransient = errors.New("embedding provider failed transiently")
	// ErrBatchTooLarge means the provider refused a call for carrying too much
	// text across its texts. The batch is embedded in halves instead.
	ErrBatchTooLarge = errors.New("embedding provider refused a batch as too large")
)

// Result is what one embedding call produced. Vectors are in the same order
// as the texts that went in; nothing downstream re-sorts them, so a provider
// that reordered its own response would corrupt every chunk's vector.
type Result struct {
	Vectors [][]float32
	Usage   domain.Usage
	// Truncated counts the texts the provider said it cut to its input limit.
	// Their vectors describe only the start of the text.
	Truncated int
}

// Embedder is the boundary the ingest and query paths depend on.
type Embedder interface {
	// Embed returns one vector per text, in order.
	Embed(ctx context.Context, kind domain.InputKind, texts []string) (Result, error)
	// Spec is the vector space this embedder writes into. A corpus pins it,
	// and a mismatch is a configuration error rather than a query that
	// returns nothing.
	Spec() domain.EmbeddingSpec
}

// backend is one provider's transport: it embeds a batch that is already
// known to be within the provider's limits. Batching, retries and ordering
// are handled once, in Client, rather than in each provider.
type backend interface {
	embed(ctx context.Context, kind domain.InputKind, texts []string) (Result, error)
}

// EmbedOne is the single-text convenience the query path uses.
func EmbedOne(ctx context.Context, embedder Embedder, kind domain.InputKind, text string) ([]float32, domain.Usage, error) {
	result, err := embedder.Embed(ctx, kind, []string{text})
	if err != nil {
		return nil, domain.Usage{}, err
	}
	if len(result.Vectors) != 1 {
		return nil, domain.Usage{}, fmt.Errorf("embedding one text returned %d vectors", len(result.Vectors))
	}
	return result.Vectors[0], result.Usage, nil
}

// normalize scales a vector to unit length.
//
// It only runs for a model the catalog does not vouch for. Cosine distance
// and inner product only agree on unit vectors, and an index built with a
// mixture of the two ranks by magnitude as much as by meaning — which looks
// like a model that is simply bad at the job.
func normalize(values []float32) {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}
	length := math.Sqrt(sum)
	if math.Abs(length-1) < 1e-6 {
		return
	}
	for index, value := range values {
		values[index] = float32(float64(value) / length)
	}
}

// truncate cuts a text down to what the model will read.
//
// The chunker already sizes chunks to the model, so this should never fire on
// an ingest. It exists for the query side, where the text is whatever a caller
// typed, and as a backstop: every provider here truncates silently, and a
// silently truncated embedding is a wrong answer with no error attached.
func truncate(text string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || chunking.EstimateTokens(text) <= maxTokens {
		return text, false
	}
	limit := min(maxTokens*4, len(text))
	for limit > 0 && chunking.EstimateTokens(text[:limit]) > maxTokens {
		limit = limit * 9 / 10
	}
	for limit > 0 && limit < len(text) && !utf8.RuneStart(text[limit]) {
		limit--
	}
	if limit <= 0 {
		return text, false
	}
	// Prefer to end on a word rather than mid-token.
	if space := strings.LastIndexAny(text[:limit], " \n\t"); space > limit/2 {
		limit = space
	}
	return text[:limit], true
}
