package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	authevents "github.com/Shubham04-oss/oryxa-go/auth/internal/events"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/httpserver"
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

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	publisher := authevents.NewPublisher(eventbridge.NewFromConfig(awsConfig), config.EventBusName, autheventsv1.Source)
	_ = application.New(publisher)

	server := &http.Server{
		Addr: config.Address,
		Handler: httpserver.New(verifier, httpserver.Config{
			CognitoDomain:   config.CognitoDomain,
			CognitoClientID: config.CognitoClientID,
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
