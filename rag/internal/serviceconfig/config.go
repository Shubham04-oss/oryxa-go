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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address            string
	AWSRegion          string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	AuthURL            string
	TeamsURL           string
	FilesURL           string
	AllowedOrigins     []string
	EventBusName       string
	EventsQueueURL     string

	// VectorBucket is the S3 Vectors bucket. Without it, corpora on that
	// backend are refused by name rather than failing at their first write.
	VectorBucket      string
	S3VectorsEndpoint string

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

	// RerankModelARN names a Bedrock reranking model. Empty leaves reranking
	// off, and a query asking for it gets the fused order instead.
	RerankModelARN string

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

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("RAG_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	embedConcurrency, err := intValue("RAG_EMBED_CONCURRENCY", 4, 1, 64)
	if err != nil {
		return Config{}, err
	}
	embedAttempts, err := intValue("RAG_EMBED_MAX_ATTEMPTS", 5, 1, 10)
	if err != nil {
		return Config{}, err
	}
	ingestBatch, err := intValue("RAG_INGEST_BATCH", 4, 1, 64)
	if err != nil {
		return Config{}, err
	}
	ingestLease, err := secondsValue("RAG_INGEST_LEASE_SECONDS", 5*time.Minute, time.Minute, time.Hour)
	if err != nil {
		return Config{}, err
	}
	ingestInterval, err := secondsValue("RAG_INGEST_INTERVAL_SECONDS", 2*time.Second, time.Second, 5*time.Minute)
	if err != nil {
		return Config{}, err
	}

	genericEndpoint := endpoint("AWS_ENDPOINT_URL")
	config := Config{
		Address:            valueOrDefault("RAG_ADDRESS", ":8080"),
		AWSRegion:          valueOrDefault("AWS_REGION", defaultRegion),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions: splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:        autoMigrate,
		AuthURL:            trimURL(os.Getenv("AUTH_URL")),
		TeamsURL:           trimURL(os.Getenv("TEAMS_URL")),
		FilesURL:           trimURL(os.Getenv("FILES_URL")),
		AllowedOrigins:     splitList(os.Getenv("RAG_ALLOWED_ORIGINS")),
		EventBusName:       valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:     strings.TrimSpace(os.Getenv("RAG_EVENTS_QUEUE_URL")),

		VectorBucket:      strings.TrimSpace(os.Getenv("RAG_VECTOR_BUCKET_NAME")),
		S3VectorsEndpoint: endpointOr("AWS_ENDPOINT_URL_S3VECTORS", genericEndpoint),

		GoogleCloudProject:  strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: valueOrDefault("GOOGLE_CLOUD_LOCATION", "global"),

		DefaultProvider: domain.Provider(valueOrDefault("RAG_DEFAULT_EMBEDDING_PROVIDER", string(domain.ProviderBedrock))),
		DefaultModel:    valueOrDefault("RAG_DEFAULT_EMBEDDING_MODEL", "amazon.titan-embed-text-v2:0"),
		DefaultMetric:   domain.Metric(valueOrDefault("RAG_DEFAULT_METRIC", string(domain.MetricCosine))),
		DefaultBackend:  domain.Backend(valueOrDefault("RAG_DEFAULT_BACKEND", string(domain.BackendPgvector))),

		AnswerProvider:     domain.Provider(valueOrDefault("RAG_ANSWER_PROVIDER", string(domain.ProviderBedrock))),
		AnswerModel:        strings.TrimSpace(os.Getenv("RAG_ANSWER_MODEL")),
		AnswerInstructions: strings.TrimSpace(os.Getenv("RAG_ANSWER_INSTRUCTIONS")),

		RerankModelARN:      strings.TrimSpace(os.Getenv("RAG_RERANK_MODEL_ARN")),
		GuardrailIdentifier: strings.TrimSpace(os.Getenv("RAG_GUARDRAIL_ID")),
		GuardrailVersion:    strings.TrimSpace(os.Getenv("RAG_GUARDRAIL_VERSION")),

		EmbedConcurrency: embedConcurrency,
		EmbedMaxAttempts: embedAttempts,
		IngestBatch:      ingestBatch,
		IngestLease:      ingestLease,
		IngestInterval:   ingestInterval,
	}

	if config.DatabaseURL == "" || config.AuthURL == "" {
		return Config{}, errors.New("DATABASE_URL and AUTH_URL are required")
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
		return Config{}, fmt.Errorf("RAG_DEFAULT_BACKEND must be %q or %q", domain.BackendPgvector, domain.BackendS3Vectors)
	}
	if err := config.DefaultBackend.Supports(spec.Metric); err != nil {
		return Config{}, err
	}
	if config.DefaultBackend == domain.BackendS3Vectors && config.VectorBucket == "" {
		return Config{}, errors.New("RAG_VECTOR_BUCKET_NAME is required when the default backend is s3vectors")
	}
	if config.DefaultProvider == domain.ProviderVertex && config.GoogleCloudProject == "" {
		return Config{}, errors.New("GOOGLE_CLOUD_PROJECT is required when the default embedding provider is vertex")
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
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func trimURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func endpoint(name string) string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/")
}

func endpointOr(name, fallback string) string {
	if value := endpoint(name); value != "" {
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

func boolValue(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return parsed, nil
}

func intValue(name string, fallback, lowest, highest int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < lowest || parsed > highest {
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", name, lowest, highest)
	}
	return parsed, nil
}

func secondsValue(name string, fallback, lowest, highest time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
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
