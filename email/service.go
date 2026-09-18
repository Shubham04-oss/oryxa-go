// Package email assembles the email service for a host. Email has no API of
// its own: it subscribes to auth's, billing's and teams' events on Amazon
// EventBridge, renders a template for each, and sends it with Amazon SES, or
// SMTP when EMAIL_SMTP_ADDR is set.
package email

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/email/internal/application"
	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
	"github.com/Shubham04-oss/oryxa-go/email/internal/mail"
	"github.com/Shubham04-oss/oryxa-go/email/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/email/internal/store"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// Name is how ORYXA_SERVICES selects email.
const Name = "email"

// Definition is how a host builds email. Email keeps nothing in PostgreSQL,
// so it has no Migrate.
var Definition = host.Definition{Name: Name, EventsQueueVariable: "EMAIL_QUEUE_URL", Mount: Mount}

// Mount assembles email on what the host shares. Polling EMAIL_QUEUE_URL is
// all this service does, so Mount refuses to start without one.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger

	templates, err := mail.ParseTemplates()
	if err != nil {
		return host.Service{}, err
	}

	storeClient := store.NewDynamoDB(dynamodb.NewFromConfig(platform.AWS), config.DeliveriesTable, config.SuppressionsTable)
	var deliveries *delivery.Deliveries
	if config.SMTPAddr != "" {
		logger.Info("sending mail over SMTP", "addr", config.SMTPAddr)
		deliveries = delivery.New(storeClient, mail.NewSMTPSender(config.SMTPAddr, config.From), logger)
	} else {
		deliveries = delivery.New(storeClient, mail.NewSESSender(sesv2.NewFromConfig(platform.AWS), config.From, config.ConfigurationSet), logger)
	}
	consumer := events.NewConsumer(application.New(templates, deliveries), logger)

	queue := sqs.NewFromConfig(platform.AWS)
	return host.Service{
		Workers: []host.Worker{func(ctx context.Context) { consumer.Poll(ctx, queue, config.QueueURL) }},
	}, nil
}
