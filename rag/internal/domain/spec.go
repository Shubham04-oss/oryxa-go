package domain

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Provider is who serves the embedding model.
type Provider string

const (
	// ProviderBedrock is Amazon Bedrock, reached with the service's own AWS
	// identity in the region it runs in.
	ProviderBedrock Provider = "bedrock"
	// ProviderVertex is Google Vertex AI, reached with application default
	// credentials in a Google Cloud project and location.
	ProviderVertex Provider = "vertex"
)

func (provider Provider) Valid() bool {
	return provider == ProviderBedrock || provider == ProviderVertex
}

// Metric is how near two vectors are held to be. It is fixed per corpus
// because the index is built for one of them.
type Metric string

const (
	// MetricCosine ignores magnitude. It is the default, and the only metric
	// every model here is trained for.
	MetricCosine Metric = "cosine"
	// MetricL2 is straight-line distance.
	MetricL2 Metric = "l2"
	// MetricInnerProduct is the dot product, which only behaves like cosine
	// when the vectors are already normalized.
	MetricInnerProduct Metric = "inner_product"
)

func (metric Metric) Valid() bool {
	switch metric {
	case MetricCosine, MetricL2, MetricInnerProduct:
		return true
	default:
		return false
	}
}

// Similarity turns a backend's distance into a score where larger is better.
//
// The mapping is monotonic per metric but not comparable across metrics or
// across corpora, which is exactly why fusion in the retrieval package ranks
// rather than adds these numbers.
func (metric Metric) Similarity(distance float64) float64 {
	switch metric {
	case MetricCosine:
		return math.Max(0, 1-distance)
	case MetricL2:
		if distance < 0 {
			distance = 0
		}
		return 1 / (1 + distance)
	case MetricInnerProduct:
		// pgvector returns the negative inner product so that smaller is
		// nearer; undo that and leave the scale alone, since an unnormalized
		// dot product has no upper bound to divide by.
		return -distance
	default:
		return 0
	}
}

// Backend is where the vectors themselves live. Both are addressed through
// one Store interface, so a corpus names which one it was written into and
// nothing above the store has to care again.
type Backend string

const (
	// BackendPgvector keeps vectors in the shared PostgreSQL, next to the
	// chunk text. It can answer hybrid queries in one statement and is the
	// default for anything an organization searches interactively.
	BackendPgvector Backend = "pgvector"
	// BackendS3Vectors keeps vectors in Amazon S3 Vectors. It is far cheaper
	// per stored vector and is where large, cold or archival corpora belong;
	// the chunk text stays in PostgreSQL either way.
	BackendS3Vectors Backend = "s3vectors"
)

func (backend Backend) Valid() bool {
	return backend == BackendPgvector || backend == BackendS3Vectors
}

// InputKind says which side of the search a text is on.
//
// Every model here is trained asymmetrically: the same sentence embedded as a
// document and as a query lands in different places on purpose. Embedding a
// query as though it were a document is the single most common way to make
// retrieval quietly mediocre, so the kind is a required argument rather than
// an option with a default.
type InputKind string

const (
	InputDocument InputKind = "document"
	InputQuery    InputKind = "query"
)

func (kind InputKind) Valid() bool {
	return kind == InputDocument || kind == InputQuery
}

// EmbeddingSpec is what a corpus pins. Two vectors only belong in one index if
// they agree on every field of it.
type EmbeddingSpec struct {
	Provider   Provider `json:"provider"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions"`
	Metric     Metric   `json:"metric"`
}

// MaxDimensions is the widest vector this service will index. pgvector's own
// ceiling for an indexed column is higher, but nothing here needs it and the
// bound keeps a typo from reserving an enormous row.
const MaxDimensions = 4096

func (spec EmbeddingSpec) Validate() error {
	if !spec.Provider.Valid() {
		return fmt.Errorf("embedding provider must be %q or %q: %w", ProviderBedrock, ProviderVertex, ErrInvalid)
	}
	if strings.TrimSpace(spec.Model) == "" {
		return fmt.Errorf("embedding model is required: %w", ErrInvalid)
	}
	if spec.Dimensions < 1 || spec.Dimensions > MaxDimensions {
		return fmt.Errorf("embedding dimensions must be between 1 and %d: %w", MaxDimensions, ErrInvalid)
	}
	if !spec.Metric.Valid() {
		return fmt.Errorf("embedding metric is not allowed: %w", ErrInvalid)
	}
	return nil
}

// Equal reports whether two specs describe the same vector space.
func (spec EmbeddingSpec) Equal(other EmbeddingSpec) bool {
	return spec.Provider == other.Provider && spec.Model == other.Model &&
		spec.Dimensions == other.Dimensions && spec.Metric == other.Metric
}

func (spec EmbeddingSpec) String() string {
	return fmt.Sprintf("%s/%s/%dd/%s", spec.Provider, spec.Model, spec.Dimensions, spec.Metric)
}

// ModelProfile is what this service knows about one embedding model without
// calling it: how wide its vectors are, how much text it will read, and how
// many texts it will take at once.
//
// The catalog below is a convenience, not a gate. A model that is not in it
// can still be used by naming its dimensions explicitly, so a model released
// after this code was written needs configuration rather than a deploy.
type ModelProfile struct {
	Provider Provider
	Model    string
	// Dimensions are the widths the model can emit. The first is its default.
	// More than one means the model was trained so its vectors can be
	// truncated, and a shorter vector costs less to store and to search.
	Dimensions []int
	// MaxInputTokens is the most text one embedding call will read. Anything
	// longer is silently truncated by the provider, which is why the chunker
	// is told about this number rather than left to guess.
	MaxInputTokens int
	// MaxBatch is how many texts one call accepts.
	MaxBatch int
	// Normalized says the model already returns unit-length vectors, so cosine
	// and inner product agree and no normalizing pass is needed.
	Normalized bool
}

func (profile ModelProfile) DefaultDimensions() int {
	if len(profile.Dimensions) == 0 {
		return 0
	}
	return profile.Dimensions[0]
}

// catalog is what we know about the models we have run. Bedrock identifiers
// are the model ids that region carries; Vertex identifiers are publisher
// model names under the project's location.
var catalog = []ModelProfile{{
	Provider:       ProviderBedrock,
	Model:          "amazon.titan-embed-text-v2:0",
	Dimensions:     []int{1024, 512, 256},
	MaxInputTokens: 8192,
	MaxBatch:       1, // Titan embeds one text per invocation.
	Normalized:     true,
}, {
	Provider:       ProviderBedrock,
	Model:          "amazon.titan-embed-text-v1",
	Dimensions:     []int{1536},
	MaxInputTokens: 8000,
	MaxBatch:       1,
	Normalized:     true,
}, {
	Provider:       ProviderBedrock,
	Model:          "cohere.embed-english-v3",
	Dimensions:     []int{1024},
	MaxInputTokens: 512,
	MaxBatch:       96,
	Normalized:     true,
}, {
	Provider:       ProviderBedrock,
	Model:          "cohere.embed-multilingual-v3",
	Dimensions:     []int{1024},
	MaxInputTokens: 512,
	MaxBatch:       96,
	Normalized:     true,
}, {
	Provider:       ProviderVertex,
	Model:          "text-embedding-005",
	Dimensions:     []int{768, 512, 256},
	MaxInputTokens: 2048,
	MaxBatch:       250,
	Normalized:     true,
}, {
	Provider:       ProviderVertex,
	Model:          "text-multilingual-embedding-002",
	Dimensions:     []int{768, 512, 256},
	MaxInputTokens: 2048,
	MaxBatch:       250,
	Normalized:     true,
}, {
	Provider:       ProviderVertex,
	Model:          "gemini-embedding-001",
	Dimensions:     []int{3072, 1536, 768},
	MaxInputTokens: 2048,
	// Vertex takes one instance per request for this model. Sending more is
	// rejected, so the batching in the embedding package reads this and does
	// not discover it in production.
	MaxBatch:   1,
	Normalized: true,
}}

// Profile returns what is known about a model, and whether anything is.
func Profile(provider Provider, model string) (ModelProfile, bool) {
	model = strings.TrimSpace(model)
	for _, entry := range catalog {
		if entry.Provider == provider && entry.Model == model {
			return entry, true
		}
	}
	return ModelProfile{}, false
}

// Catalog lists the models this service has been run against, so the API can
// offer them without a caller having to know them by heart.
func Catalog() []ModelProfile { return slices.Clone(catalog) }

// ResolveSpec turns what a caller asked for into the spec a corpus will pin.
//
// A known model fills in its own default width and rejects a width it cannot
// emit. An unknown model is allowed through, because the catalog will always
// lag the providers, but only when the caller states the dimensions: guessing
// them would build an index that fails on its first write.
func ResolveSpec(provider Provider, model string, dimensions int, metric Metric) (EmbeddingSpec, error) {
	model = strings.TrimSpace(model)
	if metric == "" {
		metric = MetricCosine
	}
	spec := EmbeddingSpec{Provider: provider, Model: model, Dimensions: dimensions, Metric: metric}
	if !provider.Valid() {
		return EmbeddingSpec{}, fmt.Errorf("embedding provider must be %q or %q: %w", ProviderBedrock, ProviderVertex, ErrInvalid)
	}
	if model == "" {
		return EmbeddingSpec{}, fmt.Errorf("embedding model is required: %w", ErrInvalid)
	}
	profile, known := Profile(provider, model)
	switch {
	case known && dimensions == 0:
		spec.Dimensions = profile.DefaultDimensions()
	case known && !slices.Contains(profile.Dimensions, dimensions):
		return EmbeddingSpec{}, fmt.Errorf("%s emits %v dimensions, not %d: %w", model, profile.Dimensions, dimensions, ErrInvalid)
	case !known && dimensions == 0:
		return EmbeddingSpec{}, fmt.Errorf("%s is not in the model catalog, so its dimensions must be given: %w", model, ErrInvalid)
	}
	if err := spec.Validate(); err != nil {
		return EmbeddingSpec{}, err
	}
	return spec, nil
}

// Strategy is how a document is cut into chunks.
type Strategy string

const (
	// StrategyStructure follows the document's own shape: headings, then
	// paragraphs, then sentences, and only splits mid-sentence when a single
	// sentence is already over budget. It is the default.
	StrategyStructure Strategy = "structure"
	// StrategyFixed cuts at the budget regardless of shape. It suits text
	// with no structure to follow, such as a transcript with no punctuation.
	StrategyFixed Strategy = "fixed"
)

func (strategy Strategy) Valid() bool {
	return strategy == StrategyStructure || strategy == StrategyFixed
}

// Chunk sizing bounds. The floor keeps a chunk large enough to carry meaning;
// the ceiling is below the shortest context any catalogued model offers, so a
// policy that validates here cannot produce a chunk the provider truncates.
const (
	MinChunkTokens     = 32
	MaxChunkTokens     = 2048
	DefaultChunkTokens = 512
	// DefaultOverlapRatio is how much of each chunk repeats the one before it.
	// Overlap is what keeps an answer that straddles a boundary retrievable.
	DefaultOverlapRatio = 0.15
)

// ChunkPolicy is how one corpus cuts its documents. It is pinned with the
// corpus: re-cutting existing documents differently would leave the index
// holding two incompatible notions of what a chunk is.
type ChunkPolicy struct {
	Strategy      Strategy `json:"strategy"`
	MaxTokens     int      `json:"max_tokens"`
	OverlapTokens int      `json:"overlap_tokens"`
}

// DefaultChunkPolicy is the policy for a corpus whose creator did not choose
// one, sized so a chunk always fits the model the corpus pins.
func DefaultChunkPolicy(spec EmbeddingSpec) ChunkPolicy {
	maxTokens := DefaultChunkTokens
	if profile, known := Profile(spec.Provider, spec.Model); known && profile.MaxInputTokens < maxTokens {
		maxTokens = profile.MaxInputTokens
	}
	return ChunkPolicy{
		Strategy:      StrategyStructure,
		MaxTokens:     maxTokens,
		OverlapTokens: int(float64(maxTokens) * DefaultOverlapRatio),
	}
}

// Normalize fills in what a caller left out and refuses what cannot work.
// The spec is needed because a chunk longer than the model reads is not a
// chunk, it is a chunk plus silently discarded text.
func (policy ChunkPolicy) Normalize(spec EmbeddingSpec) (ChunkPolicy, error) {
	fallback := DefaultChunkPolicy(spec)
	if policy.Strategy == "" {
		policy.Strategy = fallback.Strategy
	}
	if !policy.Strategy.Valid() {
		return ChunkPolicy{}, fmt.Errorf("chunk strategy is not allowed: %w", ErrInvalid)
	}
	if policy.MaxTokens == 0 {
		policy.MaxTokens = fallback.MaxTokens
		if policy.OverlapTokens == 0 {
			policy.OverlapTokens = fallback.OverlapTokens
		}
	}
	if policy.MaxTokens < MinChunkTokens || policy.MaxTokens > MaxChunkTokens {
		return ChunkPolicy{}, fmt.Errorf("chunk max_tokens must be between %d and %d: %w", MinChunkTokens, MaxChunkTokens, ErrInvalid)
	}
	if profile, known := Profile(spec.Provider, spec.Model); known && policy.MaxTokens > profile.MaxInputTokens {
		return ChunkPolicy{}, fmt.Errorf("chunk max_tokens %d is more than %s reads (%d): %w", policy.MaxTokens, spec.Model, profile.MaxInputTokens, ErrInvalid)
	}
	if policy.OverlapTokens < 0 {
		return ChunkPolicy{}, fmt.Errorf("chunk overlap_tokens cannot be negative: %w", ErrInvalid)
	}
	// Overlap at or above half the budget makes each chunk mostly a copy of
	// the last: the index grows without carrying more meaning.
	if policy.OverlapTokens*2 >= policy.MaxTokens {
		return ChunkPolicy{}, fmt.Errorf("chunk overlap_tokens must be less than half of max_tokens (%d): %w", policy.MaxTokens, ErrInvalid)
	}
	return policy, nil
}
