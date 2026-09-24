// Package provision creates what delivers auth's membership endings to
// realtime: a queue with a dead-letter queue, and an EventBridge rule allowed
// to send to it.
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

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
)

const (
	ruleName = "realtime-auth-memberships"
	targetID = "realtime-queue"
	// Receives before SQS moves a message realtime keeps failing on aside:
	// with the visibility timeout below, about 25 minutes of the Worker being
	// down, or refusing the signature, before a purge waits for a redrive.
	maxReceiveCount = 5
	// The consumer takes five messages at a time and may wait up to 30
	// seconds on the Worker for each, one after another. Five minutes covers
	// a batch the Worker never answers, and spaces the retries of one.
	visibilityTimeoutSeconds = 300
	// Fourteen days, the most SQS keeps anything, on both queues: a purge is
	// owed however late it runs, including one waiting for realtime to be
	// switched on, and a message moved to the dead-letter queue keeps the
	// age it had.
	retentionSeconds = 1209600
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
	queueName := envOrDefault("REALTIME_EVENTS_QUEUE_NAME", "realtime-events")
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
		"MessageRetentionPeriod": strconv.Itoa(retentionSeconds),
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
		"MessageRetentionPeriod": strconv.Itoa(retentionSeconds),
		"RedrivePolicy":          string(redrivePolicy),
		"SqsManagedSseEnabled":   "true",
		"VisibilityTimeout":      strconv.Itoa(visibilityTimeoutSeconds),
	})
	if err != nil {
		return Output{}, err
	}

	rule, err := eventBridgeClient.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(ruleName),
		Description:  aws.String("Auth membership endings whose conversations realtime has the web app purge"),
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

// EventPattern matches exactly the auth events realtime consumes.
func EventPattern() (string, error) {
	pattern, err := json.Marshal(map[string][]string{
		"source":      {autheventsv1.Source},
		"detail-type": slices.Sorted(slices.Values(events.AuthSubscriptions())),
	})
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
			"Sid":       "AllowRealtimeRule",
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
