package rig

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Emulators are the local AWS stand-ins the rig may touch. Nothing here ever
// holds real credentials.
type Emulators struct {
	LocalStack string // EventBridge, SQS, S3, DynamoDB
	Region     string
}

// Clients are SDK clients that can only reach the emulators.
type Clients struct {
	Events *eventbridge.Client
	SQS    *sqs.Client
	S3     *s3.Client
	Dynamo *dynamodb.Client
}

// NewClients builds emulator-only clients with static test credentials.
func NewClients(ctx context.Context, emulators Emulators) (*Clients, error) {
	// The caller pins AWS_CONFIG_FILE and AWS_SHARED_CREDENTIALS_FILE to
	// /dev/null, so no real profile is read; credentials here are static.
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(emulators.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return nil, err
	}
	endpoint := aws.String(emulators.LocalStack)
	return &Clients{
		Events: eventbridge.NewFromConfig(cfg, func(o *eventbridge.Options) { o.BaseEndpoint = endpoint }),
		SQS:    sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = endpoint }),
		S3:     s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = endpoint; o.UsePathStyle = true }),
		Dynamo: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = endpoint }),
	}, nil
}

// CreateBus makes an event bus the provisioners can hang their rules on.
func (clients *Clients) CreateBus(ctx context.Context, name string) error {
	_, err := clients.Events.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(name)})
	return err
}

// Cleanup removes everything a run created, found by its name prefix: the
// bus with its rules, queues, buckets and tables. It never touches anything
// without the prefix, so the development resources sharing the emulator are
// safe. It returns what it removed.
func (clients *Clients) Cleanup(ctx context.Context, prefix string) ([]string, error) {
	var removed, failed []string
	if prefix == "" || len(prefix) < 4 {
		return nil, fmt.Errorf("refusing to clean up with prefix %q: too short to be safe", prefix)
	}

	buses, err := clients.Events.ListEventBuses(ctx, &eventbridge.ListEventBusesInput{NamePrefix: aws.String(prefix)})
	if err == nil {
		for _, bus := range buses.EventBuses {
			name := aws.ToString(bus.Name)
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rules, _ := clients.Events.ListRules(ctx, &eventbridge.ListRulesInput{EventBusName: aws.String(name)})
			if rules != nil {
				for _, rule := range rules.Rules {
					targets, _ := clients.Events.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{Rule: rule.Name, EventBusName: aws.String(name)})
					if targets != nil && len(targets.Targets) > 0 {
						ids := make([]string, 0, len(targets.Targets))
						for _, target := range targets.Targets {
							ids = append(ids, aws.ToString(target.Id))
						}
						_, _ = clients.Events.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: rule.Name, EventBusName: aws.String(name), Ids: ids})
					}
					_, _ = clients.Events.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: rule.Name, EventBusName: aws.String(name)})
				}
			}
			if _, err := clients.Events.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(name)}); err == nil {
				removed = append(removed, "bus "+name)
			}
		}
	}

	queues, err := clients.SQS.ListQueues(ctx, &sqs.ListQueuesInput{QueueNamePrefix: aws.String(prefix)})
	if err == nil {
		for _, url := range queues.QueueUrls {
			if _, err := clients.SQS.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)}); err == nil {
				removed = append(removed, "queue "+url[strings.LastIndex(url, "/")+1:])
			}
		}
	}

	buckets, err := clients.S3.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err == nil {
		for _, bucket := range buckets.Buckets {
			name := aws.ToString(bucket.Name)
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			pager := s3.NewListObjectsV2Paginator(clients.S3, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
			for pager.HasMorePages() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					break
				}
				for _, object := range page.Contents {
					_, _ = clients.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(name), Key: object.Key})
				}
			}
			if _, err := clients.S3.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)}); err == nil {
				removed = append(removed, "bucket "+name)
			}
		}
	}

	tables, err := clients.Dynamo.ListTables(ctx, &dynamodb.ListTablesInput{})
	if err == nil {
		for _, name := range tables.TableNames {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			// The email provisioner turns deletion protection on, so it must come off first.
			_, _ = clients.Dynamo.UpdateTable(ctx, &dynamodb.UpdateTableInput{TableName: aws.String(name), DeletionProtectionEnabled: aws.Bool(false)})
			if _, err := clients.Dynamo.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(name)}); err != nil {
				failed = append(failed, "table "+name+": "+err.Error())
			} else {
				removed = append(removed, "table "+name)
			}
		}
	}
	if len(failed) > 0 {
		return removed, fmt.Errorf("could not remove %d resources: %s", len(failed), strings.Join(failed, "; "))
	}
	return removed, nil
}

// QueueDepth is how many messages wait in a queue, visible and in flight.
func (clients *Clients) QueueDepth(ctx context.Context, queueURL string) (visible, inFlight int, err error) {
	out, err := clients.SQS.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return 0, 0, err
	}
	return atoi(out.Attributes["ApproximateNumberOfMessages"]), atoi(out.Attributes["ApproximateNumberOfMessagesNotVisible"]), nil
}

func atoi(text string) int {
	n := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
