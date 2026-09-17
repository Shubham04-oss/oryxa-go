// Package auth assembles the auth service for a host: Google and password
// sign-in, browser sessions, users, organizations, memberships, invitations,
// agent grants, and the events it publishes and hears.
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/agentgrant"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/billingevents"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	authevents "github.com/Shubham04-oss/oryxa-go/auth/internal/events"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/serviceconfig"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Name is how ORYXA_SERVICES selects auth.
const Name = "auth"

// serviceAudience is the audience other services name in tokens for auth.
const serviceAudience = "auth"

// Definition is how a host builds auth.
var Definition = host.Definition{Name: Name, URLVariable: "AUTH_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the auth schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles auth on what the host shares.
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger

	provider, err := oidc.NewProvider(ctx, config.CognitoIssuer)
	if err != nil {
		return host.Service{}, fmt.Errorf("discover Cognito issuer: %w", err)
	}
	verifier := authn.New(provider, config.CognitoClientID, config.CognitoAgentClientID)
	store := postgres.New(platform.Database)
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
		return host.Service{}, err
	}

	passwords, err := passwordflow.New(
		cognitoidentityprovider.NewFromConfig(platform.AWS),
		service,
		verifier,
		passwordflow.Config{UserPoolID: config.CognitoUserPoolID, ClientID: config.CognitoClientID},
	)
	if err != nil {
		return host.Service{}, err
	}

	// The EventBridge and SQS clients sign with static "test" credentials
	// only when their endpoint is overridden for a local emulator: the
	// process otherwise carries real credentials for KMS and Cognito too, so
	// those must never be swapped out.
	eventBridgeConfig := platform.AWS
	if config.EventBridgeEndpoint != "" {
		eventBridgeConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
	}
	eventBridge := eventbridge.NewFromConfig(eventBridgeConfig, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	publisher := authevents.NewPublisher(eventBridge, platform.EventBusName, autheventsv1.Source)
	table, err := store.OutboxTable()
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
	}), logger)
	workers := []host.Worker{dispatcher.Run}

	if config.EventsQueueURL != "" {
		sqsConfig := platform.AWS
		if config.SQSEndpoint != "" {
			sqsConfig.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
		}
		queue := sqs.NewFromConfig(sqsConfig, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		consumer := billingevents.NewConsumer(service)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("billing entitlement events are disabled", "reason", "AUTH_EVENTS_QUEUE_URL is not set")
	}

	kmsClient := kms.NewFromConfig(platform.AWS)

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
			return host.Service{}, err
		}
		agentGrants = manager
	} else {
		logger.Warn("agent grants are disabled", "reason", "COGNITO_AGENT_CLIENT_ID, COGNITO_AGENT_CLIENT_SECRET, COGNITO_AGENT_CALLBACK_URL, and AUTH_KMS_KEY_ID are not all set")
	}

	var services httpserver.ServiceVerifier
	if len(config.ServiceCallers) > 0 {
		serviceVerifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Keys: kmsClient, Audience: serviceAudience, Callers: config.ServiceCallers})
		if err != nil {
			return host.Service{}, err
		}
		services = serviceVerifier
	} else {
		logger.Warn("AUTH_SERVICE_CALLERS is not set: the internal API refuses every call, so no service can mint agent tokens")
	}

	server := httpserver.New(verifier, oauth, passwords, service, httpserver.Config{
		CognitoDomain:      config.CognitoDomain,
		CognitoClientID:    config.CognitoClientID,
		CognitoCallbackURL: config.CognitoCallbackURL,
		AllowedOrigins:     config.AllowedReturnURLs,
		CookieDomain:       config.CookieDomain,
		CookieSecure:       config.CookieSecure,
		AgentClientID:      config.CognitoAgentClientID,
		AgentGrants:        agentGrants,
		Services:           services,
	})
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 15 * time.Second},
		Workers:  workers,
	}, nil
}
