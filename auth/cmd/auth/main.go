package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/billingevents"
	authevents "github.com/Shubham04-oss/oryxa-go/auth/internal/events"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/serviceconfig"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := serviceconfig.FromEnv()
	if err != nil {
		log.Fatal(err)
	}

	provider, err := oidc.NewProvider(ctx, config.CognitoIssuer)
	if err != nil {
		log.Fatalf("discover Cognito issuer: %v", err)
	}
	verifier := authn.New(provider, config.CognitoClientID)
	store, err := postgres.Open(ctx, config.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
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
		log.Printf("AUTH_EVENTS_QUEUE_URL is not set: billing entitlements are not applied, so organizations have no member limits")
	}

	server := &http.Server{
		Addr: config.Address,
		Handler: httpserver.New(verifier, oauth, passwords, service, httpserver.Config{
			CognitoDomain:      config.CognitoDomain,
			CognitoClientID:    config.CognitoClientID,
			CognitoCallbackURL: config.CognitoCallbackURL,
			AllowedOrigins:     config.AllowedReturnURLs,
			CookieDomain:       config.CookieDomain,
			CookieSecure:       config.CookieSecure,
		}),
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
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("auth listening on %s", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
