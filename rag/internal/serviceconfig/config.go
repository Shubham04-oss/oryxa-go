// Package serviceconfig reads what rag needs from the environment.
//
// Two rules shape it. Anything that decides what a corpus is — the embedding
// provider, model and backend — is validated here rather than at the first
// query, because a corpus created under a bad default cannot be fixed by
// changing the default afterwards. And anything optional degrades to a named
// missing capability rather than a nil that fails later: without a Google
// Cloud project Vertex corpora are refused by name, and without an answering
// model queries still work and answering says why it cannot.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Config is what rag reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	AuthURL  string
	TeamsURL string
	FilesURL string
	// KnowledgeURL and ServiceKeyID are how a document too large to have
	// travelled on the bus is read back. Without both, the largest documents
	// knowledge publishes stay unindexed.
	KnowledgeURL   string
	ServiceKeyID   string
	AllowedOrigins []string
	EventsQueueURL string

	// VectorBucket is the S3 Vectors bucket. Without it, corpora on that
	// backend are refused by name rather than failing at their first write.
	VectorBucket      string
	S3VectorsEndpoint string

	// KnowledgeBaseID and KnowledgeBaseDataSourceID name the one Bedrock
	// knowledge base and its one data source this deployment was given;
	// OpenTofu creates both outside rag. The backend is available only when
	// both are set. KnowledgeBaseBucket is where rag stages document text
	// too large to send inline; without it, staging is unavailable and such
	// a document is refused by name rather than failing at ingest.
	KnowledgeBaseID           string
	KnowledgeBaseDataSourceID string
	KnowledgeBaseBucket       string

	// GoogleCloudProject and GoogleCloudLocation are where Vertex AI serves
	// the models, matching how the agents service is configured.
	GoogleCloudProject  string
	GoogleCloudLocation string

	// Defaults are what a corpus gets when its creator does not choose.
	DefaultProvider domain.Provider
	DefaultModel    string
	DefaultMetric   domain.Metric
	DefaultBackend  domain.Backend

	// AnswerProvider, AnswerModel and AnswerInstructions configure grounded
	// answering. An empty model leaves answering off; searching still works.
	AnswerProvider     domain.Provider
	AnswerModel        string
	AnswerInstructions string

	// RerankModelARN names a Bedrock reranking model, and RerankVertexModel a
	// Vertex ranking model. One of them, or neither: with neither, reranking
	// is off and a query asking for it gets the fused order instead.
	RerankModelARN    string
	RerankVertexModel string

	// GuardrailIdentifier and GuardrailVersion screen answers through Amazon
	// Bedrock Guardrails, as the agents service does. The version must be a
	// numbered snapshot, never DRAFT, so what runs is what was reviewed.
	GuardrailIdentifier string
	GuardrailVersion    string

	// Embedding concurrency and retries, for a deployment whose provider
	// quota differs from the defaults.
	EmbedConcurrency int
	EmbedMaxAttempts int

	// Ingest worker settings.
	IngestBatch    int
	IngestLease    time.Duration
	IngestInterval time.Duration
}

// FromEnv reads rag's configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	embedConcurrency, err := intValue(getenv, "RAG_EMBED_CONCURRENCY", 4, 1, 64)
	if err != nil {
		return Config{}, err
	}
	embedAttempts, err := intValue(getenv, "RAG_EMBED_MAX_ATTEMPTS", 5, 1, 10)
	if err != nil {
		return Config{}, err
	}
	ingestBatch, err := intValue(getenv, "RAG_INGEST_BATCH", 4, 1, 64)
	if err != nil {
		return Config{}, err
	}
	ingestLease, err := secondsValue(getenv, "RAG_INGEST_LEASE_SECONDS", 5*time.Minute, time.Minute, time.Hour)
	if err != nil {
		return Config{}, err
	}
	ingestInterval, err := secondsValue(getenv, "RAG_INGEST_INTERVAL_SECONDS", 2*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return Config{}, err
	}

	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	config := Config{
		AuthURL:        trimURL(getenv("AUTH_URL")),
		TeamsURL:       trimURL(getenv("TEAMS_URL")),
		FilesURL:       trimURL(getenv("FILES_URL")),
		KnowledgeURL:   trimURL(getenv("KNOWLEDGE_URL")),
		ServiceKeyID:   strings.TrimSpace(getenv("RAG_SERVICE_KEY_ID")),
		AllowedOrigins: splitList(getenv("RAG_ALLOWED_ORIGINS")),
		EventsQueueURL: strings.TrimSpace(getenv("RAG_EVENTS_QUEUE_URL")),

		VectorBucket:      strings.TrimSpace(getenv("RAG_VECTOR_BUCKET_NAME")),
		S3VectorsEndpoint: endpointOr(getenv, "AWS_ENDPOINT_URL_S3VECTORS", genericEndpoint),

		KnowledgeBaseID:           strings.TrimSpace(getenv("RAG_KNOWLEDGE_BASE_ID")),
		KnowledgeBaseDataSourceID: strings.TrimSpace(getenv("RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID")),
		KnowledgeBaseBucket:       strings.TrimSpace(getenv("RAG_KNOWLEDGE_BASE_BUCKET")),

		GoogleCloudProject:  strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: valueOrDefault(getenv, "GOOGLE_CLOUD_LOCATION", "global"),

		DefaultProvider: domain.Provider(valueOrDefault(getenv, "RAG_DEFAULT_EMBEDDING_PROVIDER", string(domain.ProviderBedrock))),
		DefaultModel:    valueOrDefault(getenv, "RAG_DEFAULT_EMBEDDING_MODEL", "amazon.titan-embed-text-v2:0"),
		DefaultMetric:   domain.Metric(valueOrDefault(getenv, "RAG_DEFAULT_METRIC", string(domain.MetricCosine))),
		DefaultBackend:  domain.Backend(valueOrDefault(getenv, "RAG_DEFAULT_BACKEND", string(defaultBackend(getenv)))),

		AnswerProvider:     domain.Provider(valueOrDefault(getenv, "RAG_ANSWER_PROVIDER", string(domain.ProviderBedrock))),
		AnswerModel:        strings.TrimSpace(getenv("RAG_ANSWER_MODEL")),
		AnswerInstructions: strings.TrimSpace(getenv("RAG_ANSWER_INSTRUCTIONS")),

		RerankModelARN:      strings.TrimSpace(getenv("RAG_RERANK_MODEL_ARN")),
		RerankVertexModel:   strings.TrimSpace(getenv("RAG_RERANK_VERTEX_MODEL")),
		GuardrailIdentifier: strings.TrimSpace(getenv("RAG_GUARDRAIL_ID")),
		GuardrailVersion:    strings.TrimSpace(getenv("RAG_GUARDRAIL_VERSION")),

		EmbedConcurrency: embedConcurrency,
		EmbedMaxAttempts: embedAttempts,
		IngestBatch:      ingestBatch,
		IngestLease:      ingestLease,
		IngestInterval:   ingestInterval,
	}

	if config.AuthURL == "" {
		return Config{}, errors.New("AUTH_URL is required")
	}
	for name, value := range map[string]string{"AUTH_URL": config.AuthURL, "TEAMS_URL": config.TeamsURL, "FILES_URL": config.FilesURL} {
		if value == "" {
			continue
		}
		if parsed, err := url.Parse(value); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("%s must be an absolute URL", name)
		}
	}

	// The defaults decide what every corpus created without an explicit
	// choice will be, and a corpus cannot be changed afterwards. Validating
	// them at startup means a bad default is a failed deploy rather than a
	// set of corpora nobody can search.
	spec, err := domain.ResolveSpec(config.DefaultProvider, config.DefaultModel, 0, config.DefaultMetric)
	if err != nil {
		return Config{}, fmt.Errorf("the default embedding configuration is unusable: %w", err)
	}
	if !config.DefaultBackend.Valid() {
		return Config{}, fmt.Errorf("RAG_DEFAULT_BACKEND must be %q, %q or %q", domain.BackendPgvector, domain.BackendS3Vectors, domain.BackendBedrockKB)
	}
	if err := config.DefaultBackend.Supports(spec.Metric); err != nil {
		return Config{}, err
	}
	if required, ok := config.DefaultBackend.RequiredEmbeddingSpec(); ok && !spec.Equal(required) {
		return Config{}, fmt.Errorf("the default embedding configuration is unusable: %s corpora use the knowledge base's own embedding spec (%s), not %s",
			config.DefaultBackend, required, spec)
	}
	if config.DefaultBackend == domain.BackendS3Vectors && config.VectorBucket == "" {
		return Config{}, errors.New("RAG_VECTOR_BUCKET_NAME is required when the default backend is s3vectors")
	}
	if config.DefaultBackend == domain.BackendBedrockKB && (config.KnowledgeBaseID == "" || config.KnowledgeBaseDataSourceID == "") {
		return Config{}, errors.New("RAG_KNOWLEDGE_BASE_ID and RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID are required when the default backend is bedrock-kb")
	}
	if config.DefaultProvider == domain.ProviderVertex && config.GoogleCloudProject == "" {
		return Config{}, errors.New("GOOGLE_CLOUD_PROJECT is required when the default embedding provider is vertex")
	}
	if (config.KnowledgeBaseID == "") != (config.KnowledgeBaseDataSourceID == "") {
		return Config{}, errors.New("RAG_KNOWLEDGE_BASE_ID and RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID must be set together")
	}

	if !config.AnswerProvider.Valid() {
		return Config{}, fmt.Errorf("RAG_ANSWER_PROVIDER must be %q or %q", domain.ProviderBedrock, domain.ProviderVertex)
	}
	if config.AnswerModel != "" && config.AnswerProvider == domain.ProviderVertex && config.GoogleCloudProject == "" {
		return Config{}, errors.New("GOOGLE_CLOUD_PROJECT is required when the answering provider is vertex")
	}
	if config.GuardrailIdentifier != "" {
		if config.GuardrailVersion == "" {
			return Config{}, errors.New("RAG_GUARDRAIL_VERSION is required when RAG_GUARDRAIL_ID is set")
		}
		if strings.EqualFold(config.GuardrailVersion, "DRAFT") {
			return Config{}, errors.New("RAG_GUARDRAIL_VERSION must be a numbered snapshot, not DRAFT")
		}
	}
	if config.RerankModelARN != "" && !strings.HasPrefix(config.RerankModelARN, "arn:") {
		return Config{}, errors.New("RAG_RERANK_MODEL_ARN must be a model ARN")
	}
	if config.RerankModelARN != "" && config.RerankVertexModel != "" {
		return Config{}, errors.New("set RAG_RERANK_MODEL_ARN or RAG_RERANK_VERTEX_MODEL, not both: rag reranks with one model")
	}
	if config.RerankVertexModel != "" && config.GoogleCloudProject == "" {
		return Config{}, errors.New("RAG_RERANK_VERTEX_MODEL needs GOOGLE_CLOUD_PROJECT: the Ranking API bills a project")
	}
	return config, nil
}

// defaultBackend is where a new corpus's vectors go when nothing says: S3
// Vectors wherever a vector bucket is configured — serverless, priced by use,
// and keeping the vectors' bulk out of the shared PostgreSQL — and pgvector
// where none is, which is local development and tests, since S3 Vectors has
// no local emulation. A corpus keeps the backend it was created on.
func defaultBackend(getenv func(string) string) domain.Backend {
	if strings.TrimSpace(getenv("RAG_VECTOR_BUCKET_NAME")) != "" {
		return domain.BackendS3Vectors
	}
	return domain.BackendPgvector
}

func valueOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func trimURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func endpoint(getenv func(string) string, name string) string {
	return strings.TrimRight(strings.TrimSpace(getenv(name)), "/")
}

func endpointOr(getenv func(string) string, name, fallback string) string {
	if value := endpoint(getenv, name); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func intValue(getenv func(string) string, name string, fallback, lowest, highest int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < lowest || parsed > highest {
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", name, lowest, highest)
	}
	return parsed, nil
}

func secondsValue(getenv func(string) string, name string, fallback, lowest, highest time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number of seconds", name)
	}
	duration := time.Duration(parsed) * time.Second
	if duration < lowest || duration > highest {
		return 0, fmt.Errorf("%s must be between %d and %d seconds", name, int(lowest.Seconds()), int(highest.Seconds()))
	}
	return duration, nil
}
