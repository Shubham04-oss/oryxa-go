package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/email/internal/events"
)

const (
	// defaultBus is the only bus SES publishes its events to.
	defaultBus = "default"

	// Receives before SQS moves a message the consumer keeps failing on to the
	// dead-letter queue.
	maxReceiveCount = 5
	// AWS recommends six times the consumer's function timeout, which keeps
	// that timeout at 10 seconds or less.
	visibilityTimeoutSeconds = 60
)

var tags = map[string]string{
	"application": "oryxa",
	"managed-by":  "oryxa-go",
}

func sortedTagKeys() []string {
	return slices.Sorted(maps.Keys(tags))
}

type Clients struct {
	DynamoDB    *dynamodb.Client
	EventBridge *eventbridge.Client
	SES         *sesv2.Client
	SQS         *sqs.Client
	// SESEventBridge is EventBridge in the SES region, whose default bus is
	// the only one SES can publish to. Nil means SES runs in the same region
	// as everything else, and EventBridge is used.
	SESEventBridge *eventbridge.Client
}

type DNSRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Output struct {
	Region               string      `json:"region"`
	DeliveriesTable      string      `json:"deliveries_table"`
	SuppressionsTable    string      `json:"suppressions_table"`
	QueueURL             string      `json:"queue_url"`
	QueueARN             string      `json:"queue_arn"`
	DeadLetterQueueURL   string      `json:"dead_letter_queue_url"`
	EventBusName         string      `json:"event_bus_name"`
	SubscriptionsRuleARN string      `json:"subscriptions_rule_arn"`
	FeedbackRuleARN      string      `json:"feedback_rule_arn"`
	SESIdentity          string      `json:"ses_identity"`
	SESVerified          bool        `json:"ses_verified"`
	SESConfigurationSet  string      `json:"ses_configuration_set"`
	SESEventPublishing   bool        `json:"ses_event_publishing"`
	DKIMRecords          []DNSRecord `json:"dkim_records,omitempty"`
}

func Run(ctx context.Context, clients Clients, config Config) (Output, error) {
	subscriptionsPattern, err := EventPattern(events.Subscriptions())
	if err != nil {
		return Output{}, err
	}
	feedbackPattern, err := FeedbackPattern(config.ConfigurationSet)
	if err != nil {
		return Output{}, err
	}

	if err := ensureTable(ctx, clients.DynamoDB, deliveriesTable(config.DeliveriesTable), deliveryExpiryAttribute); err != nil {
		return Output{}, err
	}
	if err := ensureTable(ctx, clients.DynamoDB, suppressionsTable(config.SuppressionsTable), ""); err != nil {
		return Output{}, err
	}

	deadLetterURL, deadLetterARN, err := ensureQueue(ctx, clients.SQS, config.DeadLetterQueueName, map[string]string{
		"MessageRetentionPeriod": "1209600",
		"SqsManagedSseEnabled":   "true",
	})
	if err != nil {
		return Output{}, err
	}
	redrivePolicy, err := json.Marshal(map[string]any{
		"deadLetterTargetArn": deadLetterARN,
		"maxReceiveCount":     maxReceiveCount,
	})
	if err != nil {
		return Output{}, fmt.Errorf("encode redrive policy: %w", err)
	}
	queueURL, queueARN, err := ensureQueue(ctx, clients.SQS, config.QueueName, map[string]string{
		"RedrivePolicy":        string(redrivePolicy),
		"SqsManagedSseEnabled": "true",
		"VisibilityTimeout":    strconv.Itoa(visibilityTimeoutSeconds),
	})
	if err != nil {
		return Output{}, err
	}

	subscriptions := rule{
		name:        subscriptionsRuleName,
		bus:         config.EventBusName,
		description: "Oryxa domain events the email service sends mail for",
		pattern:     subscriptionsPattern,
	}
	feedback := rule{
		name:        feedbackRuleName,
		bus:         defaultBus,
		description: "SES events about mail the email service sent",
		pattern:     feedbackPattern,
	}
	var ruleARNs []string
	for _, eventRule := range []rule{subscriptions, feedback} {
		arn, err := ensureRule(ctx, clients.EventBridge, eventRule)
		if err != nil {
			return Output{}, err
		}
		ruleARNs = append(ruleARNs, arn)
	}
	if err := allowRules(ctx, clients.SQS, queueURL, queueARN, ruleARNs); err != nil {
		return Output{}, err
	}
	for _, eventRule := range []rule{subscriptions, feedback} {
		if err := ensureTarget(ctx, clients.EventBridge, eventRule, queueARN); err != nil {
			return Output{}, err
		}
	}

	verified, records, err := ensureIdentity(ctx, clients.SES, config.SESIdentity)
	if err != nil {
		return Output{}, err
	}
	if config.SESEventPublishing {
		if err := ensureConfigurationSet(ctx, clients.SES, config.ConfigurationSet); err != nil {
			return Output{}, err
		}
		sesBus := clients.SESEventBridge
		if sesBus == nil {
			sesBus = clients.EventBridge
		}
		defaultBusARN, err := busARN(ctx, sesBus, defaultBus)
		if err != nil {
			return Output{}, err
		}
		if err := ensureEventDestination(ctx, clients.SES, config.ConfigurationSet, defaultBusARN); err != nil {
			return Output{}, err
		}
	}

	return Output{
		Region:               config.Region,
		DeliveriesTable:      config.DeliveriesTable,
		SuppressionsTable:    config.SuppressionsTable,
		QueueURL:             queueURL,
		QueueARN:             queueARN,
		DeadLetterQueueURL:   deadLetterURL,
		EventBusName:         config.EventBusName,
		SubscriptionsRuleARN: ruleARNs[0],
		FeedbackRuleARN:      ruleARNs[1],
		SESIdentity:          config.SESIdentity,
		SESVerified:          verified,
		SESConfigurationSet:  config.ConfigurationSet,
		SESEventPublishing:   config.SESEventPublishing,
		DKIMRecords:          records,
	}, nil
}
