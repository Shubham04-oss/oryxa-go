package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/credentials"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/dynamo"
	intevents "github.com/Shubham04-oss/oryxa-go/integrations/internal/events"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/execution"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filebroker"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/lifecycle"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piecerunner"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	githubprovider "github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/github"
	googleprovider "github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/google"
	microsoftprovider "github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoft"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/microsoftgraph"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers/slack"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/queue"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhookroutes"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-integrations"})
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
	config, err := serviceconfig.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	provider, err := oidc.NewProvider(ctx, config.CognitoIssuer)
	if err != nil {
		log.Fatalf("discover Cognito issuer: %v", err)
	}
	store, err := postgres.Open(ctx, config.DatabaseURL, config.PostgresExtensions...)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	slog.Info("postgres connected", "extensions", store.ExtensionVersions())
	if config.AutoMigrate {
		if err := store.Migrate(ctx); err != nil {
			log.Fatal(err)
		}
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(config.AWSRegion)}
	if config.DynamoEndpoint != "" || config.SQSEndpoint != "" || config.EventBridgeEndpoint != "" || config.KMSEndpoint != "" || config.LambdaEndpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	dynamoClient := dynamodb.NewFromConfig(awsConfig, func(options *dynamodb.Options) {
		if config.DynamoEndpoint != "" {
			options.BaseEndpoint = aws.String(config.DynamoEndpoint)
		}
	})
	sqsClient := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) {
		if config.SQSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.SQSEndpoint)
		}
	})
	lambdaClient := lambda.NewFromConfig(awsConfig, func(options *lambda.Options) {
		if config.LambdaEndpoint != "" {
			options.BaseEndpoint = aws.String(config.LambdaEndpoint)
		}
	})
	kmsClient := kms.NewFromConfig(awsConfig, func(options *kms.Options) {
		if config.KMSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.KMSEndpoint)
		}
	})
	eventClient := eventbridge.NewFromConfig(awsConfig, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})

	ids := func(prefix string) (string, error) {
		value := make([]byte, 18)
		if _, err := rand.Read(value); err != nil {
			return "", err
		}
		return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
	}
	secretBox := secrets.Envelope{Keys: secrets.KMSDataKeys{Client: kmsClient, KeyID: config.KMSKeyID}, KeyID: config.KMSKeyID}
	providerRegistry := oauthflow.StaticProviders{
		"slack": slack.OAuth{}, "github": githubprovider.OAuth{}, "google": googleprovider.OAuth{},
		"microsoft": microsoftprovider.OAuth{},
	}
	credentialSource := credentials.Manager{Store: store, Secrets: secretBox,
		Providers: providerRegistry, IDs: ids}
	catalog := catalog.Static{PublicBaseURL: strings.TrimRight(config.PublicBaseURL, "/")}
	routes := dynamo.Routes{Client: dynamoClient, TableName: config.DynamoTable}
	state := dynamo.TriggerState{Client: dynamoClient, TableName: config.DynamoTable}
	runner := piecerunner.Client{Lambda: lambdaClient, FunctionName: config.PieceRunnerFunction}
	graphClient := microsoftgraph.Client{}
	githubClient := githubprovider.Client{}
	googleClient := googleprovider.Client{}
	oauth := oauthflow.Service{Catalog: catalog, Authorizer: store, Profiles: store,
		States: dynamo.OAuthStates{Client: dynamoClient, TableName: config.DynamoTable}, Connections: store,
		Secrets: secretBox, Providers: providerRegistry,
		ReturnURLs: oauthflow.AllowedReturnURLs(config.AllowedReturnURLs), IDs: ids}
	webhookRouteService := webhookroutes.Service{Authorizer: store, Profiles: store, Catalog: catalog,
		Routes: routes, Secrets: secretBox, PublicURL: strings.TrimRight(config.PublicBaseURL, "/")}
	triggerService := triggerflow.Service{Catalog: catalog, Authorizer: store, Connections: store,
		Routes: routes, Store: store, State: state, Secrets: secretBox, Credentials: credentialSource, Runner: runner,
		MicrosoftGraph: graphClient, GitHub: githubClient, Google: googleClient, GmailCensus: store,
		GmailPubSubTopic: config.GmailPubSubTopic,
		IDs:              ids, PublicURL: strings.TrimRight(config.PublicBaseURL, "/")}
	claims := dynamo.Claims{Client: dynamoClient, TableName: config.DynamoTable, IDs: ids}
	webhookQueue := queue.WebhookQueue{Client: sqsClient, QueueURL: config.WebhookQueueURL}
	webhookService := webhooks.Service{Routes: routes, Secrets: secretBox,
		Runner: githubprovider.WebhookRunner{Fallback: googleprovider.WebhookRunner{Fallback: microsoftgraph.WebhookRunner{Fallback: runner}}},
		Claims: claims, Queue: webhookQueue, IDs: ids}
	var gmailPushes httpserver.GmailPushAdmission
	if config.GmailPubSubTopic != "" {
		audience := strings.TrimRight(config.PublicBaseURL, "/") + "/v1/webhooks/google/gmail"
		verifier, err := googleprovider.NewIDTokenVerifier(ctx, audience, config.GmailPushServiceAccount)
		if err != nil {
			log.Fatalf("configure Gmail push verifier: %v", err)
		}
		gmailPushes = googleprovider.PushService{Verifier: verifier, Triggers: store, Claims: claims,
			Queue: webhookQueue, IDs: ids, Audience: audience}
	}
	executor := execution.Service{Store: store, State: state, Secrets: secretBox, Credentials: credentialSource, Runner: runner,
		Mapper: execution.DomainMapper{}, IDs: ids, PublicURL: strings.TrimRight(config.PublicBaseURL, "/")}
	actions := actionflow.Service{Catalog: catalog, Authorizer: store, Connections: store,
		Store: store, Credentials: credentialSource, Runner: runner,
		Mapper: actionflow.DomainMapper{}, IDs: ids, PublicURL: strings.TrimRight(config.PublicBaseURL, "/")}
	var pieceFiles *filebroker.Handler
	if config.FilesURL != "" {
		files := filesclient.New(config.FilesURL)
		sessions := dynamo.FileSessions{Client: dynamoClient, TableName: config.DynamoTable}
		pieceFiles = &filebroker.Handler{Sessions: sessions, Files: files}
		actions.Files = files
		actions.FileSessions = sessions
		actions.FileBrokerURL = strings.TrimRight(config.PublicBaseURL, "/") + "/v1/internal/piece-files"
	}
	renewals := triggerflow.RenewalWorker{References: store, Renewer: triggerService, Lease: store,
		ReportError: func(err error) { slog.Error("renew Microsoft Graph trigger", "error", err) }}
	graphCleanup := triggerflow.MicrosoftGraphCleanupWorker{Jobs: store, Routes: routes, State: state,
		Secrets: secretBox, Graph: graphClient,
		ReportError: func(err error) { slog.Error("clean Microsoft Graph trigger", "error", err) }}
	githubCleanup := triggerflow.GitHubCleanupWorker{Jobs: store, Routes: routes, State: state,
		Secrets: secretBox, GitHub: githubClient,
		ReportError: func(err error) { slog.Error("clean GitHub trigger", "error", err) }}
	googleCleanup := triggerflow.GoogleCleanupWorker{Jobs: store, Routes: routes, State: state,
		Secrets: secretBox, Google: googleClient, Census: store,
		ReportError: func(err error) { slog.Error("clean Google trigger", "error", err) }}
	go (queue.Consumer{Client: sqsClient, QueueURL: config.WebhookQueueURL, Processor: executor}).Run(ctx)
	go (intevents.Dispatcher{Store: store, Publisher: intevents.Publisher{Client: eventClient, BusName: config.EventBusName}}).Run(ctx)
	if config.EventsQueueURL != "" {
		go intevents.NewConsumer(lifecycle.Service{Store: store, IDs: ids}, slog.Default()).Poll(ctx, sqsClient, config.EventsQueueURL)
	} else {
		slog.Warn("INTEGRATIONS_EVENTS_QUEUE_URL is not set: integrations will not hear about deleted organizations or members leaving them")
	}
	go renewals.Run(ctx)
	go graphCleanup.Run(ctx)
	go githubCleanup.Run(ctx)
	go googleCleanup.Run(ctx)

	handler := observability.HTTPHandler("oryxa-integrations", httpserver.NewAPI(httpserver.Dependencies{Authenticator: authn.BearerAuthenticator{Verifier: authn.New(provider, config.CognitoClientID)}, Catalog: oauth, OAuth: oauth, WebhookRoutes: webhookRouteService, Triggers: triggerService, Webhooks: webhookService, GmailPushes: gmailPushes, Actions: actions, PieceFiles: pieceFiles}))
	server := &http.Server{Addr: config.Address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("shutdown integrations", "error", err)
		}
	}()
	slog.Info("integrations listening", "address", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
