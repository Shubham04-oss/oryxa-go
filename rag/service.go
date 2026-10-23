// Package rag assembles the rag service for a host: retrieval-augmented
// generation for an organization — its corpora, the documents in them, and
// the grounded answers they support.
//
// Two workers run beside its HTTP API: the outbox dispatcher that publishes
// rag's own events, and the ingest pipeline that turns an accepted document
// into searchable chunks. The pipeline claims its work from PostgreSQL with a
// lease rather than working what a request just submitted, so a submission
// returns as soon as its text is safely stored and more than one process may
// run the pipeline safely.
package rag

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/events"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/ingest"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgebase"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgeclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// Name is how ORYXA_SERVICES selects rag.
const Name = "rag"

// URLVariable is how another service reaches rag, such as agents searching
// a member's corpora.
const URLVariable = "RAG_URL"

// Definition is how a host builds rag.
var Definition = host.Definition{Name: Name, URLVariable: URLVariable, EventsQueueVariable: "RAG_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the rag schema.
//
// It must not check that pgvector is installed: on a fresh database the pool
// it is handed can predate the migration that installs the extension, and
// Store's Migrate applies rag's SQL without looking for it — only Open does.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	if err := postgres.New(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	// The access decision log, which rag records into; whichever service
	// migrates first installs it.
	return access.Migrate(ctx, platform.Database)
}

// Mount assembles rag on what the host shares.
//
// It refuses to mount when the shared pool has no pgvector. Every corpus
// keeps its chunk text in PostgreSQL and its vectors in pgvector or S3
// Vectors depending on what it named, but the chunk and vector tables are
// created together, so pgvector is required whatever a deployment configures
// S3 Vectors for. This is the first point after every migration has run where
// that can be checked against a live snapshot, rather than the one Migrate
// saw, which can predate it.
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	extensions := platform.Database.ExtensionVersions()
	pgvectorVersion, ok := extensions["vector"]
	if !ok {
		return host.Service{}, errors.New("rag requires the pgvector extension, which is not installed on this database")
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	store := postgres.New(platform.Database)

	bedrockClient := bedrockruntime.NewFromConfig(platform.AWS)
	bedrockAgentRuntimeClient := bedrockagentruntime.NewFromConfig(platform.AWS)

	// Every backend is built where it can be, and a corpus naming one that
	// is not configured is refused by name rather than failing at its first
	// write.
	stores := []vectorstore.Store{vectorstore.NewPgvector(store.Pool(), pgvectorVersion)}
	if config.VectorBucket != "" {
		s3VectorsClient := s3vectors.NewFromConfig(platform.AWS, func(options *s3vectors.Options) {
			if config.S3VectorsEndpoint != "" {
				options.BaseEndpoint = aws.String(config.S3VectorsEndpoint)
			}
		})
		s3Store, err := vectorstore.NewS3Vectors(s3VectorsClient, config.VectorBucket)
		if err != nil {
			return host.Service{}, err
		}
		stores = append(stores, s3Store)
	} else {
		logger.Warn("RAG_VECTOR_BUCKET_NAME is not set: corpora cannot be created on S3 Vectors")
	}

	// The knowledge base itself, when this deployment was given one: ingest
	// and application both need it to submit, poll and remove documents, and
	// retrieval needs its own, separate client to search it (see
	// rag/internal/retrieval/bedrock_kb.go) — a different Bedrock API from
	// the one this package manages documents through.
	//
	// ingestKnowledgeBase and appKnowledgeBase are declared as their
	// consumers' interfaces, left at their true nil rather than assigned a
	// nil *knowledgebase.Client, because an interface holding a nil pointer
	// is not itself nil: pipeline.knowledgeBase == nil and
	// service.knowledgeBase == nil would both read false and every method
	// promptly panic on a nil receiver.
	var (
		knowledgeBaseRetriever *retrieval.KnowledgeBase
		ingestKnowledgeBase    ingest.KnowledgeBase
		appKnowledgeBase       application.KnowledgeBase
	)
	if config.KnowledgeBaseID != "" && config.KnowledgeBaseDataSourceID != "" {
		var objects knowledgebase.Objects
		if config.KnowledgeBaseBucket != "" {
			objects = s3.NewFromConfig(platform.AWS)
		} else {
			logger.Warn("RAG_KNOWLEDGE_BASE_BUCKET is not set: a bedrock-kb document too large to send inline cannot be indexed")
		}
		knowledgeBaseClient, err := knowledgebase.New(bedrockagent.NewFromConfig(platform.AWS), objects, knowledgebase.Config{
			KnowledgeBaseID: config.KnowledgeBaseID, DataSourceID: config.KnowledgeBaseDataSourceID, Bucket: config.KnowledgeBaseBucket,
		})
		if err != nil {
			return host.Service{}, err
		}
		if config.KnowledgeBaseManaged {
			knowledgeBaseRetriever, err = retrieval.NewManagedKnowledgeBase(bedrockAgentRuntimeClient, config.KnowledgeBaseID)
		} else {
			knowledgeBaseRetriever, err = retrieval.NewKnowledgeBase(bedrockAgentRuntimeClient, config.KnowledgeBaseID, config.RerankModelARN)
		}
		if err != nil {
			return host.Service{}, err
		}
		ingestKnowledgeBase, appKnowledgeBase = knowledgeBaseClient, knowledgeBaseClient
		stores = append(stores, vectorstore.BedrockKnowledgeBase{})
	} else {
		logger.Warn("RAG_KNOWLEDGE_BASE_ID and RAG_KNOWLEDGE_BASE_DATA_SOURCE_ID are not set: corpora cannot be created on the bedrock-kb backend")
	}
	vectors := vectorstore.NewRouter(stores...)

	embedders := embedding.NewRegistry(embedding.Config{
		Bedrock: bedrockClient,
		Vertex: embedding.VertexConfig{
			Project:     config.GoogleCloudProject,
			Location:    config.GoogleCloudLocation,
			Credentials: platform.Google,
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
	switch {
	case config.RerankModelARN != "":
		reranker, err = retrieval.NewBedrockReranker(bedrockAgentRuntimeClient, config.RerankModelARN)
		if err != nil {
			return host.Service{}, err
		}
	case config.RerankVertexModel != "":
		reranker, err = retrieval.NewVertexReranker(ctx, retrieval.VertexRerankConfig{
			Project: config.GoogleCloudProject, Model: config.RerankVertexModel, Credentials: platform.Google,
		})
		if err != nil {
			return host.Service{}, err
		}
	default:
		logger.Info("RAG_RERANK_MODEL_ARN and RAG_RERANK_VERTEX_MODEL are not set: queries asking to rerank get the fused order")
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
			Credentials: platform.Google,
		})
	default:
		generator, err = generation.NewBedrock(bedrockClient, config.AnswerModel, generation.BedrockOptions{
			GuardrailIdentifier: config.GuardrailIdentifier, GuardrailVersion: config.GuardrailVersion,
		})
	}
	if err != nil {
		return host.Service{}, err
	}

	retriever := retrieval.New(store, vectors, embedders, reranker, knowledgeBaseRetriever, logger)

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

	// A document over knowledge's event budget travels without its body.
	// Reading it back is what keeps the largest documents an organization
	// has — the handbook, the specification, the long thread — from being
	// the ones it cannot search.
	var documents application.Documents
	switch {
	case config.KnowledgeURL == "":
		logger.Warn("KNOWLEDGE_URL is not set: a knowledge document too large to carry on the bus cannot be read back")
	case config.ServiceKeyID == "":
		logger.Warn("RAG_SERVICE_KEY_ID is not set: a knowledge document too large to carry on the bus cannot be read back, " +
			"since reading it means proving which service is asking")
	default:
		documents = knowledgeclient.New(knowledgeclient.Config{
			BaseURL: config.KnowledgeURL,
			Signer: serviceauth.Signer{
				Client: kms.NewFromConfig(platform.AWS), KeyID: config.ServiceKeyID, Service: Name,
			},
		})
	}

	service := application.New(store, authclient.New(config.AuthURL), teams, files, documents, vectors,
		retriever, generator, appKnowledgeBase, application.Defaults{
			Provider:           config.DefaultProvider,
			Model:              config.DefaultModel,
			Metric:             config.DefaultMetric,
			Backend:            config.DefaultBackend,
			AnswerInstructions: config.AnswerInstructions,
		}, logger).WithDecisions(access.NewLog(platform.Database.Pool, logger)).WithShadow(platform.Shadow)

	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}
	table, err := outbox.NewTable(store.Pool(), "rag_outbox")
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{
			ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data,
			TraceContext: event.TraceContext,
		})
	}), logger)
	pipeline := ingest.New(store, vectors, embedders, ingestKnowledgeBase, ingest.Options{
		Batch: config.IngestBatch, Lease: config.IngestLease, Interval: config.IngestInterval,
	}, logger)
	workers := []host.Worker{dispatcher.Run, pipeline.Run}
	if config.EventsQueueURL != "" {
		consumer, err := events.NewConsumer(service, platform.EventAuth.Verifier(), logger)
		if err != nil {
			return host.Service{}, err
		}
		queue := sqs.NewFromConfig(platform.AWS)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("RAG_EVENTS_QUEUE_URL is not set: rag will not hear about deleted organizations, members leaving them, deleted teams, deleted files, or documents knowledge publishes or removes")
	}

	server := httpserver.New(service, httpserver.Config{
		AllowedOrigins: config.AllowedOrigins,
		Capabilities: httpserver.Capabilities{
			Providers:    embedders.Providers(),
			Backends:     vectors.Backends(),
			Models:       domain.Catalog(),
			ContentTypes: domain.TextContentTypes(),
			Answering:    generator != nil,
			Reranking:    reranker != nil,
		},
	}, logger)

	logger.Info("rag mounted",
		"backends", vectors.Backends(),
		"embedding_providers", embedders.Providers(),
		"answering", generator != nil,
		"knowledge_base", config.KnowledgeBaseID,
		"knowledge_base_managed", config.KnowledgeBaseManaged)

	return host.Service{
		Handler: server,
		Routes:  server.Patterns(),
		Timeouts: host.Timeouts{
			Read: 60 * time.Second,
			// Answering runs a retrieval and then a model, so it is the
			// slowest thing this service does.
			Write: 180 * time.Second,
		},
		Workers: workers,
	}, nil
}
