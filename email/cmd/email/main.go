package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/email/internal/application"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
	"github.com/Shubham04-oss/oryxa-go/email/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/email/internal/store"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-email"})
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
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	templates, err := mail.ParseTemplates()
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.Default()
	storeClient := store.NewDynamoDB(dynamodb.NewFromConfig(awsConfig), config.DeliveriesTable, config.SuppressionsTable)
	var deliveries *delivery.Deliveries
	if config.SMTPAddr != "" {
		logger.InfoContext(ctx, "sending mail over SMTP", "addr", config.SMTPAddr)
		deliveries = delivery.New(storeClient, mail.NewSMTPSender(config.SMTPAddr, config.From), logger)
	} else {
		deliveries = delivery.New(storeClient, mail.NewSESSender(sesv2.NewFromConfig(awsConfig), config.From, config.ConfigurationSet), logger)
	}
	consumer := events.NewConsumer(application.New(templates, deliveries), logger)

	// The Lambda runtime sets AWS_LAMBDA_RUNTIME_API. Anywhere else, poll the
	// same queue the function's event source mapping reads.
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.StartWithOptions(consumer.HandleSQS, lambda.WithContext(ctx))
		return
	}
	if config.QueueURL == "" {
		log.Fatal("EMAIL_QUEUE_URL is required outside Lambda")
	}
	logger.InfoContext(ctx, "polling for email events", "queue_url", config.QueueURL)
	consumer.Poll(ctx, sqs.NewFromConfig(awsConfig), config.QueueURL)
}
