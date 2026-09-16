package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/agentgrant"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/billingevents"
	authevents "github.com/Shubham04-oss/oryxa-go/auth/internal/events"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/serviceconfig"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// serviceAudience is the audience other services name in tokens for auth.
const serviceAudience = "auth"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-auth"})
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
	verifier := authn.New(provider, config.CognitoClientID, config.CognitoAgentClientID)
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
	service := application.NewWithConfig(store, application.Config{
		InvitationAcceptURL:  config.InvitationAcceptURL,
		EmailVerificationURL: config.EmailVerificationURL,
		PasswordResetURL:     config.PasswordResetURL,
	})
	oauth, err := oauthflow.New(oauthflow.Config{
		CognitoDomain:   config.CognitoDomain,
		ClientID:        config.CognitoClientID,
		CallbackURL:     config.CognitoCallbackURL,
		AllowedReturnTo: config.AllowedReturnURLs,
	}, service, verifier)
	if err != nil {
		log.Fatal(err)
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	passwords, err := passwordflow.New(
		cognitoidentityprovider.NewFromConfig(awsConfig),
		service,
		verifier,
		passwordflow.Config{UserPoolID: config.CognitoUserPoolID, ClientID: config.CognitoClientID},
	)
	if err != nil {
		log.Fatal(err)
	}
	eventBridgeConfig := awsConfig
	if config.EventBridgeEndpoint != "" {
		eventBridgeConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
	}
	eventBridge := eventbridge.NewFromConfig(eventBridgeConfig, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	publisher := authevents.NewPublisher(eventBridge, config.EventBusName, autheventsv1.Source)
	dispatcher := authevents.NewDispatcher(store, publisher)
	go dispatcher.Run(ctx)

	if config.EventsQueueURL != "" {
		sqsConfig := awsConfig
		if config.SQSEndpoint != "" {
			sqsConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		}
		queue := sqs.NewFromConfig(sqsConfig, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		go billingevents.NewConsumer(service).Poll(ctx, queue, config.EventsQueueURL)
	} else {
		slog.Warn("billing entitlement events are disabled", "reason", "AUTH_EVENTS_QUEUE_URL is not set")
	}

	kmsClient := kms.NewFromConfig(awsConfig)

	var agentGrants httpserver.AgentGrantsFlow
	if config.AgentGrantsEnabled() {
		manager, err := agentgrant.New(agentgrant.Config{
			CognitoDomain:   config.CognitoDomain,
			ClientID:        config.CognitoAgentClientID,
			ClientSecret:    config.CognitoAgentClientSecret,
			CallbackURL:     config.CognitoAgentCallbackURL,
			AllowedReturnTo: config.AllowedReturnURLs,
		}, store, verifier, secrets.Envelope{Keys: secrets.KMSDataKeys{Client: kmsClient, KeyID: config.AuthKMSKeyID}, KeyID: config.AuthKMSKeyID})
		if err != nil {
			log.Fatal(err)
		}
		agentGrants = manager
	} else {
		slog.Warn("agent grants are disabled", "reason", "COGNITO_AGENT_CLIENT_ID, COGNITO_AGENT_CLIENT_SECRET, COGNITO_AGENT_CALLBACK_URL, and AUTH_KMS_KEY_ID are not all set")
	}

	var services httpserver.ServiceVerifier
	if len(config.ServiceCallers) > 0 {
		serviceVerifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Keys: kmsClient, Audience: serviceAudience, Callers: config.ServiceCallers})
		if err != nil {
			log.Fatal(err)
		}
		services = serviceVerifier
	} else {
		slog.Warn("AUTH_SERVICE_CALLERS is not set: the internal API refuses every call, so no service can mint agent tokens")
	}

	server := &http.Server{
		Addr: config.Address,
		Handler: observability.HTTPHandler("oryxa-auth", httpserver.New(verifier, oauth, passwords, service, httpserver.Config{
			CognitoDomain:      config.CognitoDomain,
			CognitoClientID:    config.CognitoClientID,
			CognitoCallbackURL: config.CognitoCallbackURL,
			AllowedOrigins:     config.AllowedReturnURLs,
			CookieDomain:       config.CookieDomain,
			CookieSecure:       config.CookieSecure,
			AgentClientID:      config.CognitoAgentClientID,
			AgentGrants:        agentGrants,
			Services:           services,
		})),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("shutdown auth", "error", err)
		}
	}()

	slog.Info("auth listening", "address", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
