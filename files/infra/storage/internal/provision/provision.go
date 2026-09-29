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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/events"
)

const (
	ruleName                 = "files-auth-organizations"
	targetID                 = "files-queue"
	maxReceiveCount          = 5
	visibilityTimeoutSeconds = 60
	lifecycleExpireDays      = 90
	abortMultipartDays       = 1
)

// recoverableDeleteDays is how long an object that was deleted or replaced
// stays recoverable. OpenTofu versions the bucket
// (deploy/production/hardening.tf); S3 removes an older version this many days
// after it stopped being the newest, and then the delete marker left behind.
const recoverableDeleteDays = 30

var tags = map[string]string{
	"application": "oryxa",
	"managed-by":  "oryxa-go",
}

type Config struct {
	Region              string
	EventBusName        string
	QueueName           string
	DeadLetterQueueName string
	BucketName          string
}

func FromEnv() Config {
	queueName := envOrDefault("FILES_EVENTS_QUEUE_NAME", "files-events")
	return Config{
		Region:              envOrDefault("AWS_REGION", "ap-south-1"),
		EventBusName:        envOrDefault("EVENT_BUS_NAME", "default"),
		QueueName:           queueName,
		DeadLetterQueueName: queueName + "-dlq",
		BucketName:          envOrDefault("FILES_BUCKET_NAME", "oryxa-files"),
	}
}

type Output struct {
	Region             string `json:"region"`
	EventBusName       string `json:"event_bus_name"`
	RuleARN            string `json:"rule_arn"`
	QueueURL           string `json:"queue_url"`
	QueueARN           string `json:"queue_arn"`
	DeadLetterQueueURL string `json:"dead_letter_queue_url"`
	BucketName         string `json:"bucket_name"`
}

type Clients struct {
	EventBridge *eventbridge.Client
	SQS         *sqs.Client
	S3          *s3.Client
}

func Run(ctx context.Context, clients Clients, config Config) (Output, error) {
	if err := ensureBucket(ctx, clients.S3, config); err != nil {
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
		Description:  aws.String("Auth organization and teams events files keeps up with"),
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
		BucketName:         config.BucketName,
	}, nil
}

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
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode event pattern: %w", err)
	}
	return string(pattern), nil
}

func QueuePolicy(queueARN, ruleARN string) (string, error) {
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "AllowFilesRule",
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

func ensureBucket(ctx context.Context, client *s3.Client, config Config) error {
	if config.BucketName == "" {
		return errors.New("FILES_BUCKET_NAME is required")
	}
	input := &s3.CreateBucketInput{
		Bucket:          aws.String(config.BucketName),
		ObjectOwnership: s3types.ObjectOwnershipBucketOwnerEnforced,
	}
	if config.Region != "" && config.Region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(config.Region),
		}
	}
	_, err := client.CreateBucket(ctx, input)
	if err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
			return fmt.Errorf("create bucket %s: %w", config.BucketName, err)
		}
	}
	if _, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(config.BucketName),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("block public access on %s: %w", config.BucketName, err)
	}
	if _, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(config.BucketName),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
			Rules: []s3types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
					SSEAlgorithm: s3types.ServerSideEncryptionAes256,
				},
				BucketKeyEnabled: aws.Bool(true),
			}},
		},
	}); err != nil {
		return fmt.Errorf("encrypt bucket %s: %w", config.BucketName, err)
	}
	if _, err := client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(config.BucketName),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{{
				ID:     aws.String("expire-objects"),
				Status: s3types.ExpirationStatusEnabled,
				Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String("org/")},
				Expiration: &s3types.LifecycleExpiration{
					Days: aws.Int32(lifecycleExpireDays),
				},
				AbortIncompleteMultipartUpload: &s3types.AbortIncompleteMultipartUpload{
					DaysAfterInitiation: aws.Int32(abortMultipartDays),
				},
			}, {
				ID:     aws.String("recoverable-deletes"),
				Status: s3types.ExpirationStatusEnabled,
				Filter: &s3types.LifecycleRuleFilter{},
				NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{
					NoncurrentDays: aws.Int32(recoverableDeleteDays),
				},
				Expiration: &s3types.LifecycleExpiration{
					ExpiredObjectDeleteMarker: aws.Bool(true),
				},
			}},
		},
	}); err != nil {
		return fmt.Errorf("lifecycle on %s: %w", config.BucketName, err)
	}
	if _, err := client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(config.BucketName),
		Tagging: &s3types.Tagging{TagSet: []s3types.Tag{
			{Key: aws.String("application"), Value: aws.String("oryxa")},
			{Key: aws.String("managed-by"), Value: aws.String("oryxa-go")},
			{Key: aws.String("service"), Value: aws.String("files")},
		}},
	}); err != nil {
		return fmt.Errorf("tag bucket %s: %w", config.BucketName, err)
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
