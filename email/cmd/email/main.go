package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/email/internal/application"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
	"github.com/Shubham04-oss/oryxa-go/email/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/email/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := serviceconfig.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	templates, err := mail.ParseTemplates()
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	deliveries := delivery.New(
		store.NewDynamoDB(dynamodb.NewFromConfig(awsConfig), config.DeliveriesTable, config.SuppressionsTable),
		mail.NewSESSender(sesv2.NewFromConfig(awsConfig), config.From, config.ConfigurationSet),
		logger,
	)
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
