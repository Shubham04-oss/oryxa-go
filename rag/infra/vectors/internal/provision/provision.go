// Package provision creates the AWS resources rag owns: the S3 Vectors bucket
// its cold corpora live in, the queue it hears about deleted things on, and
// the EventBridge rule that fills that queue.
//
// It is repeatable. Everything here is created if missing and updated if
// present, so it can be run after a deploy, after a LocalStack restart, or
// just to check nothing has drifted.
//
// The pgvector side needs nothing here: those tables are service migrations,
// which the process applies itself at startup.
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
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	s3vectorstypes "github.com/aws/aws-sdk-go-v2/service/s3vectors/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/events"
)

const (
	ruleName        = "rag-lifecycle"
	targetID        = "rag-queue"
	maxReceiveCount = 5
	// Indexing a document can take a while, and this queue only carries
	// deletions, which are fast. Sixty seconds is ample for the handler and
	// short enough that a lost message comes back quickly.
	visibilityTimeoutSeconds = 60
)

var tags = map[string]string{
	"application": "oryxa",
	"managed-by":  "oryxa-go",
	"service":     "rag",
}

type Config struct {
	Region              string
	EventBusName        string
	QueueName           string
	DeadLetterQueueName string
	// VectorBucketName is the S3 Vectors bucket. Empty skips it, for a
	// deployment that only uses pgvector.
	VectorBucketName string
	// KMSKeyARN encrypts the vector bucket with a customer-managed key.
	// Empty leaves it on the service-managed default.
	KMSKeyARN string
}

func FromEnv() Config {
	queueName := envOrDefault("RAG_EVENTS_QUEUE_NAME", "rag-events")
	return Config{
		Region:              envOrDefault("AWS_REGION", "ap-south-1"),
		EventBusName:        envOrDefault("EVENT_BUS_NAME", "default"),
		QueueName:           queueName,
		DeadLetterQueueName: queueName + "-dlq",
		VectorBucketName:    strings.TrimSpace(os.Getenv("RAG_VECTOR_BUCKET_NAME")),
		KMSKeyARN:           strings.TrimSpace(os.Getenv("RAG_VECTOR_BUCKET_KMS_KEY_ARN")),
	}
}

type Output struct {
	Region             string `json:"region"`
	EventBusName       string `json:"event_bus_name"`
	RuleARN            string `json:"rule_arn"`
	QueueURL           string `json:"queue_url"`
	QueueARN           string `json:"queue_arn"`
	DeadLetterQueueURL string `json:"dead_letter_queue_url"`
	VectorBucketName   string `json:"vector_bucket_name,omitempty"`
}

type Clients struct {
	EventBridge *eventbridge.Client
	SQS         *sqs.Client
	S3Vectors   *s3vectors.Client
}

func Run(ctx context.Context, clients Clients, config Config) (Output, error) {
	if err := ensureVectorBucket(ctx, clients.S3Vectors, config); err != nil {
		return Output{}, err
	}
	pattern, err := EventPattern()
	if err != nil {
		return Output{}, err
	}
	deadLetterURL, deadLetterARN, err := ensureQueue(ctx, clients.SQS, config.DeadLetterQueueName, map[string]string{
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
	queueURL, queueARN, err := ensureQueue(ctx, clients.SQS, config.QueueName, map[string]string{
		"RedrivePolicy":        string(redrivePolicy),
		"SqsManagedSseEnabled": "true",
		"VisibilityTimeout":    strconv.Itoa(visibilityTimeoutSeconds),
	})
	if err != nil {
		return Output{}, err
	}

	rule, err := clients.EventBridge.PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String(ruleName),
		Description:  aws.String("What rag must index or drop: documents knowledge published or removed, and what rag indexed that is now gone (deleted organizations, members, teams and files)"),
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
	if _, err := clients.SQS.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(queueURL),
		Attributes: map[string]string{"Policy": policy},
	}); err != nil {
		return Output{}, fmt.Errorf("set queue policy: %w", err)
	}
	targets, err := clients.EventBridge.PutTargets(ctx, &eventbridge.PutTargetsInput{
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
		VectorBucketName:   config.VectorBucketName,
	}, nil
}

// EventPattern is what rag listens for.
//
// Most of these are something rag indexed going away: without the rule the
// index quietly outlives what it was built from, which is the failure that
// matters most here, because it keeps answering questions from text nobody is
// entitled to see any more. Knowledge's two are the opposite direction — a
// document to index or update — but travel on the same queue, since nothing
// about receiving them differs from receiving a deletion.
func EventPattern() (string, error) {
	pattern, err := json.Marshal(map[string]any{
		"$or": []map[string][]string{
			{
				"source":      {autheventsv1.Source},
				"detail-type": slices.Sorted(slices.Values(events.AuthSubscriptions())),
			},
			{
				"source":      {teamseventsv1.Source},
				"detail-type": slices.Sorted(slices.Values(events.TeamSubscriptions())),
			},
			{
				"source":      {fileseventsv1.Source},
				"detail-type": slices.Sorted(slices.Values(events.FileSubscriptions())),
			},
			{
				"source":      {knowledgeeventsv1.Source},
				"detail-type": slices.Sorted(slices.Values(events.KnowledgeSubscriptions())),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode event pattern: %w", err)
	}
	return string(pattern), nil
}

// QueuePolicy admits this rule and nothing else.
func QueuePolicy(queueARN, ruleARN string) (string, error) {
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "AllowRagRule",
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

// ensureVectorBucket creates the S3 Vectors bucket if it is not there.
//
// The per-corpus indexes inside it are not created here: a corpus's index
// depends on its pinned width and metric, which are not known until the corpus
// exists, so the ingest path creates it on first write.
func ensureVectorBucket(ctx context.Context, client *s3vectors.Client, config Config) error {
	if config.VectorBucketName == "" {
		return nil
	}
	if client == nil {
		return errors.New("an S3 Vectors client is required to provision a vector bucket")
	}
	input := &s3vectors.CreateVectorBucketInput{
		VectorBucketName: aws.String(config.VectorBucketName),
		Tags:             tags,
	}
	if config.KMSKeyARN != "" {
		input.EncryptionConfiguration = &s3vectorstypes.EncryptionConfiguration{
			SseType:   s3vectorstypes.SseTypeAwsKms,
			KmsKeyArn: aws.String(config.KMSKeyARN),
		}
	}
	if _, err := client.CreateVectorBucket(ctx, input); err != nil {
		// Already there is the outcome this wanted.
		var conflict *s3vectorstypes.ConflictException
		if errors.As(err, &conflict) || strings.Contains(err.Error(), "ConflictException") {
			return nil
		}
		return fmt.Errorf("create vector bucket %s: %w", config.VectorBucketName, err)
	}
	return nil
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
