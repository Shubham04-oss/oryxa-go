// Command rag serves retrieval-augmented generation for an organization: its
// corpora, the documents in them, and the grounded answers they support.
//
// One process runs both the HTTP API and the ingest worker. They are separable
// — the worker claims its work from PostgreSQL with a lease, so several
// processes can run safely — but a single deployment is what most
// organizations need and splitting it later costs nothing.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/events"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-rag"})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownContext); err != nil {
			slog.Error("shutdown telemetry", "error", err)
		}
	}()
	if err := run(ctx); err != nil {
		slog.Error("rag stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := slog.Default()
	config, err := serviceconfig.FromEnv()
	if err != nil {
		return err
	}

	store, err := postgres.Open(ctx, config.DatabaseURL, config.PostgresExtensions...)
	if err != nil {
		return err
	}
	defer store.Close()
	logger.Info("postgres connected", "extensions", store.ExtensionVersions())
	if config.AutoMigrate {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		return err
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	bedrockClient := bedrockruntime.NewFromConfig(awsConfig)

	// Both vector backends are built where they can be, and a corpus naming
	// one that is not configured is refused by name rather than failing at
	// its first write.
	stores := []vectorstore.Store{vectorstore.NewPgvector(store.Pool(), store.PgvectorVersion())}
	if config.VectorBucket != "" {
		s3VectorsClient := s3vectors.NewFromConfig(awsConfig, func(options *s3vectors.Options) {
			if config.S3VectorsEndpoint != "" {
				options.BaseEndpoint = aws.String(config.S3VectorsEndpoint)
			}
		})
		s3Store, err := vectorstore.NewS3Vectors(s3VectorsClient, config.VectorBucket)
		if err != nil {
			return err
		}
		stores = append(stores, s3Store)
	} else {
		logger.Warn("RAG_VECTOR_BUCKET_NAME is not set: corpora cannot be created on S3 Vectors")
	}
	vectors := vectorstore.NewRouter(stores...)

	embedders := embedding.NewRegistry(embedding.Config{
		Bedrock: bedrockClient,
		Vertex: embedding.VertexConfig{
			Project:  config.GoogleCloudProject,
			Location: config.GoogleCloudLocation,
		},
		Options: embedding.Options{
			Concurrency: config.EmbedConcurrency,
			MaxAttempts: config.EmbedMaxAttempts,
			Logger:      logger,
		},
	})
	if config.GoogleCloudProject == "" {
		logger.Warn("GOOGLE_CLOUD_PROJECT is not set: corpora cannot be created on Vertex AI models")
	}

	var reranker retrieval.Reranker
	if config.RerankModelARN != "" {
		reranker, err = retrieval.NewBedrockReranker(bedrockagentruntime.NewFromConfig(awsConfig), config.RerankModelARN)
		if err != nil {
			return err
		}
	} else {
		logger.Info("RAG_RERANK_MODEL_ARN is not set: queries asking to rerank get the fused order")
	}

	// Answering is optional. Without a model, searching still works and
	// answering says why it cannot, which beats a service that will not start.
	var generator generation.Generator
	switch {
	case config.AnswerModel == "":
		logger.Warn("RAG_ANSWER_MODEL is not set: rag will retrieve but not answer")
	case config.AnswerProvider == domain.ProviderVertex:
		generator, err = generation.NewVertex(ctx, generation.VertexConfig{
			Project: config.GoogleCloudProject, Location: config.GoogleCloudLocation, Model: config.AnswerModel,
		})
	default:
		generator, err = generation.NewBedrock(bedrockClient, config.AnswerModel, generation.BedrockOptions{
			GuardrailIdentifier: config.GuardrailIdentifier, GuardrailVersion: config.GuardrailVersion,
		})
	}
	if err != nil {
		return err
	}

	retriever := retrieval.New(store, vectors, embedders, reranker, logger)

	var teams application.Teams
	if config.TeamsURL != "" {
		teams = teamsclient.New(config.TeamsURL)
	} else {
		logger.Warn("TEAMS_URL is not set: team-scoped corpora cannot be created")
	}
	var files application.Files
	if config.FilesURL != "" {
		files = filesclient.New(config.FilesURL)
	} else {
		logger.Warn("FILES_URL is not set: documents can only be submitted as text")
	}

	service := application.New(store, authclient.New(config.AuthURL), teams, files, vectors,
		retriever, generator, application.Defaults{
			Provider:           config.DefaultProvider,
			Model:              config.DefaultModel,
			Metric:             config.DefaultMetric,
			Backend:            config.DefaultBackend,
			AnswerInstructions: config.AnswerInstructions,
		}, logger)

	dispatcher := events.NewDispatcher(store, events.NewPublisher(eventbridge.NewFromConfig(awsConfig), config.EventBusName), logger)
	go dispatcher.Run(ctx)

	pipeline := ingest.New(store, vectors, embedders, ingest.Options{
		Batch: config.IngestBatch, Lease: config.IngestLease, Interval: config.IngestInterval,
	}, logger)
	go pipeline.Run(ctx)

	if config.EventsQueueURL != "" {
		go events.NewConsumer(service, logger).Poll(ctx, sqs.NewFromConfig(awsConfig), config.EventsQueueURL)
	} else {
		logger.Warn("RAG_EVENTS_QUEUE_URL is not set: rag will not hear about deleted organizations, members leaving them, deleted teams or deleted files")
	}

	server := &http.Server{
		Addr: config.Address,
		Handler: observability.HTTPHandler("oryxa-rag", httpserver.New(service, httpserver.Config{
			AllowedOrigins: config.AllowedOrigins,
			Capabilities: httpserver.Capabilities{
				Providers:    embedders.Providers(),
				Backends:     vectors.Backends(),
				Models:       domain.Catalog(),
				ContentTypes: domain.TextContentTypes(),
				Answering:    generator != nil,
				Reranking:    reranker != nil,
			},
		}, logger)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		// Answering runs a retrieval and then a model, so it is the slowest
		// thing this service does.
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("shutdown", "error", err)
		}
	}()

	logger.Info("rag listening",
		"address", config.Address,
		"backends", vectors.Backends(),
		"embedding_providers", embedders.Providers(),
		"answering", generator != nil)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
