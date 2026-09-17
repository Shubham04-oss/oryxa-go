// Package provision creates what delivers the auth events that make
// knowledge retire what it has indexed: a queue with a dead-letter queue, and
// an EventBridge rule allowed to send to it.
package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	eventbridgetypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
)

const (
	ruleName = "knowledge-events"
	targetID = "knowledge-queue"
	// Receives before SQS moves a message knowledge keeps failing on aside.
	maxReceiveCount          = 5
	visibilityTimeoutSeconds = 60
)

var tags = map[string]string{
	"application": "oryxa",
	"managed-by":  "oryxa-go",
}

type Config struct {
	Region              string
	EventBusName        string
	QueueName           string
	DeadLetterQueueName string
}

func FromEnv() Config {
	queueName := envOrDefault("KNOWLEDGE_EVENTS_QUEUE_NAME", "knowledge-events")
	return Config{
		Region:              envOrDefault("AWS_REGION", "ap-south-1"),
		EventBusName:        envOrDefault("EVENT_BUS_NAME", "default"),
		QueueName:           queueName,
		DeadLetterQueueName: queueName + "-dlq",
	}
}

type Output struct {
	Region             string `json:"region"`
	EventBusName       string `json:"event_bus_name"`
	RuleARN            string `json:"rule_arn"`
	QueueURL           string `json:"queue_url"`
	QueueARN           string `json:"queue_arn"`
	DeadLetterQueueURL string `json:"dead_letter_queue_url"`
}

// Run is repeatable: it creates what is missing and brings the rest in line.
func Run(ctx context.Context, eventBridgeClient *eventbridge.Client, sqsClient *sqs.Client, config Config) (Output, error) {
	pattern, err := EventPattern()
	if err != nil {
		return Output{}, err
	}
	deadLetterURL, deadLetterARN, err := ensureQueue(ctx, sqsClient, config.DeadLetterQueueName, map[string]string{
		"MessageRetentionPeriod": "1209600",
		"SqsManagedSseEnabled":   "true",
	})
	if err != nil {
		return Output{}, err
	}
	redrivePolicy, err := json.Marshal(map[string]any{"deadLetterTargetArn": deadLetterARN, "maxReceiveCount": maxReceiveCount})
	if err != nil {
		return Output{}, fmt.Errorf("encode redrive policy: %w", err)
	}
	queueURL, queueARN, err := ensureQueue(ctx, sqsClient, config.QueueName, map[string]string{
		"RedrivePolicy":        string(redrivePolicy),
		"SqsManagedSseEnabled": "true",
		"VisibilityTimeout":    strconv.Itoa(visibilityTimeoutSeconds),
	})
	if err != nil {
		return Output{}, err
	}

	rule, err := eventBridgeClient.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(ruleName),
		Description:  aws.String("Auth events that make knowledge retire what it has indexed"),
		EventBusName: aws.String(config.EventBusName),
		EventPattern: aws.String(pattern),
		State:        eventbridgetypes.RuleStateEnabled,
		Tags:         ruleTags(),
	})
	if err != nil {
		return Output{}, fmt.Errorf("put EventBridge rule: %w", err)
	}
	ruleARN := aws.ToString(rule.RuleArn)
	policy, err := QueuePolicy(queueARN, ruleARN)
	if err != nil {
		return Output{}, err
	}
	if _, err := sqsClient.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(queueURL),
		Attributes: map[string]string{"Policy": policy},
	}); err != nil {
		return Output{}, fmt.Errorf("set queue policy: %w", err)
	}
	targets, err := eventBridgeClient.PutTargets(ctx, &eventbridge.PutTargetsInput{
		Rule:         aws.String(ruleName),
		EventBusName: aws.String(config.EventBusName),
		Targets:      []eventbridgetypes.Target{{Id: aws.String(targetID), Arn: aws.String(queueARN)}},
	})
	if err != nil {
		return Output{}, fmt.Errorf("put EventBridge target: %w", err)
	}
	if targets.FailedEntryCount > 0 {
		entry := targets.FailedEntries[0]
		return Output{}, fmt.Errorf("put EventBridge target failed: %s: %s", aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
	}

	return Output{
		Region:             config.Region,
		EventBusName:       config.EventBusName,
		RuleARN:            ruleARN,
		QueueURL:           queueURL,
		QueueARN:           queueARN,
		DeadLetterQueueURL: deadLetterURL,
	}, nil
}

// EventPattern matches exactly the events knowledge consumes.
func EventPattern() (string, error) {
	subscriptions := events.Subscriptions()
	var sources []map[string][]string
	for _, source := range slices.Sorted(maps.Keys(subscriptions)) {
		sources = append(sources, map[string][]string{
			"source":      {source},
			"detail-type": slices.Sorted(slices.Values(subscriptions[source])),
		})
	}
	pattern, err := json.Marshal(map[string]any{"$or": sources})
	if err != nil {
		return "", fmt.Errorf("encode event pattern: %w", err)
	}
	return string(pattern), nil
}

// QueuePolicy lets the rule, and nothing else, send to the queue.
func QueuePolicy(queueARN, ruleARN string) (string, error) {
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "AllowKnowledgeRule",
			"Effect":    "Allow",
			"Principal": map[string]string{"Service": "events.amazonaws.com"},
			"Action":    "sqs:SendMessage",
			"Resource":  queueARN,
			"Condition": map[string]any{"ArnEquals": map[string]string{"aws:SourceArn": ruleARN}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("encode queue policy: %w", err)
	}
	return string(policy), nil
}

func ensureQueue(ctx context.Context, client *sqs.Client, name string, attributes map[string]string) (string, string, error) {
	var queueURL string
	existing, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err == nil {
		queueURL = aws.ToString(existing.QueueUrl)
		if _, err := client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: existing.QueueUrl, Attributes: attributes}); err != nil {
			return "", "", fmt.Errorf("update queue %s: %w", name, err)
		}
	} else {
		var notFound *sqstypes.QueueDoesNotExist
		if !errors.As(err, &notFound) {
			return "", "", fmt.Errorf("find queue %s: %w", name, err)
		}
		created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes, Tags: tags})
		if err != nil {
			return "", "", fmt.Errorf("create queue %s: %w", name, err)
		}
		queueURL = aws.ToString(created.QueueUrl)
	}
	output, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", "", fmt.Errorf("read queue %s ARN: %w", name, err)
	}
	return queueURL, output.Attributes[string(sqstypes.QueueAttributeNameQueueArn)], nil
}

func ruleTags() []eventbridgetypes.Tag {
	var result []eventbridgetypes.Tag
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		result = append(result, eventbridgetypes.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return result
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
