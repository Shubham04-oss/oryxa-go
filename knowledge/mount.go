package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/membertoken"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/judge"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/originals"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// Definition is how a host builds knowledge. Rag reads back a document too
// large to have travelled on the bus from knowledge's internal API, at
// KNOWLEDGE_URL, which defaults to this process when it runs knowledge.
var Definition = host.Definition{Name: Name, URLVariable: "KNOWLEDGE_URL", EventsQueueVariable: "KNOWLEDGE_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs knowledge's schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles knowledge on what the host shares. GitHub, Notion, Slack,
// Google Drive and Gmail are mounted once COMPOSIO_API_KEY says app sources
// are on; files, which reads as the member who registered it, once FILES_URL
// and KNOWLEDGE_SERVICE_KEY_ID give it a way to act as one; and a PDF or an
// Office file is kept rather than skipped once KNOWLEDGE_ORIGINALS_BUCKET
// names somewhere to keep it.
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if len(config.AllowedOrigins) == 0 {
		logger.Warn("KNOWLEDGE_ALLOWED_ORIGINS is not set: members can manage sources only with bearer tokens")
	}

	var apps application.Apps
	extractors := sources.Registry{}
	if config.ComposioAPIKey != "" {
		composioClient, err := composio.New(composio.Config{APIKey: config.ComposioAPIKey, BaseURL: config.ComposioBaseURL})
		if err != nil {
			return host.Service{}, err
		}
		apps = appsclient.New(config.AppsURL)
		for provider, extractor := range Builtin(composioClient) {
			extractors[provider] = extractor
		}
	} else {
		logger.Warn("COMPOSIO_API_KEY is not set: app sources (GitHub, Notion, Slack, Google Drive, Gmail) are off")
	}

	// Acting as a member while they are away is what the files source reads
	// with, and what registering a newly connected app's contents for them
	// needs. Auth's standing grant fills that, redeemed with knowledge's own
	// signed identity.
	var grants application.Grants
	if config.ServiceKeyID != "" {
		grants = memberGrants{tokens: membertoken.New(membertoken.Config{
			AuthURL: config.AuthURL,
			Signer: serviceauth.Signer{
				Client: kms.NewFromConfig(platform.AWS), KeyID: config.ServiceKeyID, Service: Name,
			},
		})}
	} else {
		logger.Warn("KNOWLEDGE_SERVICE_KEY_ID is not set: knowledge cannot act as a member while they are away, " +
			"so neither the organization's own files nor what a newly connected app holds are indexed for them")
	}
	// A document knowledge does not read itself — a PDF, an Office file —
	// keeps its bytes here instead of being skipped outright, once there is
	// somewhere configured to put them.
	var documentOriginals application.Originals
	if config.OriginalsBucket != "" {
		documentOriginals = originals.NewS3FromConfig(platform.AWS, config.S3Endpoint, config.OriginalsBucket)
	} else {
		logger.Warn("KNOWLEDGE_ORIGINALS_BUCKET is not set: a PDF or an Office file is skipped rather than kept")
	}

	// What a connected app yields is judged before it is published, once
	// there is a model to judge with.
	documentJudge, err := judgeFor(ctx, config, platform)
	if err != nil {
		return host.Service{}, err
	}
	if documentJudge == nil {
		logger.Warn("KNOWLEDGE_JUDGE is off: documents from connected apps are indexed without judging whether they are worth it, " +
			"so only Gmail's own Promotions and Social categories and empty documents are kept out")
	}

	// Files is the one source with nobody to act as: every route there wants
	// a member's own credentials, and a scheduled pass has none.
	switch {
	case config.FilesURL == "":
		logger.Warn("FILES_URL is not set: the organization's own files are not indexed")
	case grants != nil:
		var fileOptions []sources.FilesOption
		if documentOriginals != nil {
			fileOptions = append(fileOptions, sources.WithOriginals())
		}
		files := sources.NewFiles(filesclient.New(config.FilesURL), fileOptions...)
		extractors[files.Provider()] = files
	}

	// A retrieval pipeline reads back a body too large to have travelled on
	// the bus through the internal API. Without named callers there is no
	// internal API, and the largest documents stay unindexed.
	var serviceVerifier httpserver.ServiceVerifier
	if len(config.ServiceCallers) > 0 {
		verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
			Keys: kms.NewFromConfig(platform.AWS), Audience: Name, Callers: config.ServiceCallers,
		})
		if err != nil {
			return host.Service{}, err
		}
		serviceVerifier = verifier
	} else {
		logger.Warn("KNOWLEDGE_SERVICE_CALLERS is not set: a document too large to carry on the bus cannot be read back, so the largest documents are not indexed")
	}

	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventClient, platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}
	dependencies := Dependencies{
		Store:     postgres.New(platform.Database),
		Directory: authclient.New(config.AuthURL),
		Apps:      apps,
		Grants:    grants,
		Sink:      publisher,
		// One verifier covers every source knowledge hears from: it
		// resolves the signing key from the source an event names, so a
		// signature made with one service's key cannot be replayed as
		// another's by editing the envelope.
		Verifier:   platform.EventAuth.Verifier(),
		Extractors: extractors,
		Originals:  documentOriginals,
		Judge:      documentJudge,
		Logger:     logger,
	}
	dependencies.Policy = platform.WorkspaceSettings()
	service, err := New(dependencies, Config{
		AllowedOrigins: config.AllowedOrigins, FullSweepInterval: config.FullSweepInterval,
		Services: serviceVerifier, OriginalsBucket: config.OriginalsBucket,
	})
	if err != nil {
		return host.Service{}, err
	}

	workers := make([]host.Worker, 0, len(service.Workers())+1)
	for _, worker := range service.Workers() {
		workers = append(workers, host.Worker(worker))
	}
	if config.EventsQueueURL != "" {
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		consumer := service.Consumer()
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("KNOWLEDGE_EVENTS_QUEUE_URL is not set: knowledge will not pause or retire sources when their connection, member or organization goes away")
	}

	return host.Service{
		Handler:  service.Handler(),
		Routes:   service.Routes(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers:  workers,
	}, nil
}

// memberTokens is what memberGrants asks; *membertoken.Client answers it.
type memberTokens interface {
	MemberToken(ctx context.Context, organizationID, memberID string) (string, error)
}

// memberGrants puts auth's answers about a member's grant in knowledge's own
// terms, so work that needed a token can tell a member who has to grant again
// from a deployment that may not ask, and both from an auth that is only
// down for a moment — the one of the three worth trying again.
type memberGrants struct{ tokens memberTokens }

func (grants memberGrants) MemberToken(ctx context.Context, organizationID, memberID string) (string, error) {
	token, err := grants.tokens.MemberToken(ctx, organizationID, memberID)
	switch {
	case err == nil:
		return token, nil
	case errors.Is(err, membertoken.ErrGrantRequired):
		return "", fmt.Errorf("%w: %w", domain.ErrGrantRequired, err)
	case errors.Is(err, membertoken.ErrNotConfigured):
		return "", fmt.Errorf("%w: %w", domain.ErrForbidden, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", err
	default:
		return "", fmt.Errorf("%w: %w", domain.ErrProviderUnavailable, err)
	}
}

// judgeFor builds the judge KNOWLEDGE_JUDGE names: Jev, with the Gemini model,
// when there is one, deciding what Jev is unsure of and what it cannot read;
// or the Gemini model alone. Nil when judging is off.
func judgeFor(ctx context.Context, config serviceconfig.Config, platform host.Platform) (application.Judge, error) {
	var gemini *judge.Gemini
	if config.Judge != serviceconfig.JudgeOff && config.JudgeModel != "" {
		var err error
		gemini, err = judge.New(ctx, judge.Config{
			Project: config.GoogleCloudProject, Location: config.GoogleCloudLocation,
			Model: config.JudgeModel, Credentials: platform.Google,
		})
		if err != nil {
			return nil, err
		}
	}
	switch config.Judge {
	case serviceconfig.JudgeJev:
		jevConfig := judge.JevConfig{
			APIKey: config.TypeSafeAPIKey, BaseURL: config.TypeSafeBaseURL, Model: config.JudgeJevModel,
			EscalateBelow: config.JudgeEscalateBelow,
		}
		if gemini != nil {
			jevConfig.Escalate = gemini
		}
		jev, err := judge.NewJev(jevConfig)
		if err != nil {
			return nil, err
		}
		return jev, nil
	case serviceconfig.JudgeGemini:
		if gemini == nil {
			return nil, nil
		}
		return gemini, nil
	default:
		return nil, nil
	}
}
