package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func ensureQueue(ctx context.Context, client *sqs.Client, name string, attributes map[string]string) (string, string, error) {
	var queueURL string
	existing, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err == nil {
		queueURL = aws.ToString(existing.QueueUrl)
		_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
			QueueUrl:   existing.QueueUrl,
			Attributes: attributes,
		})
		if err != nil {
			return "", "", fmt.Errorf("update SQS queue %s: %w", name, err)
		}
	} else {
		var notFound *types.QueueDoesNotExist
		if !errors.As(err, &notFound) {
			return "", "", fmt.Errorf("find SQS queue %s: %w", name, err)
		}
		created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName:  aws.String(name),
			Attributes: attributes,
			Tags:       tags,
		})
		if err != nil {
			return "", "", fmt.Errorf("create SQS queue %s: %w", name, err)
		}
		queueURL = aws.ToString(created.QueueUrl)
	}

	output, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", "", fmt.Errorf("read SQS queue %s ARN: %w", name, err)
	}
	return queueURL, output.Attributes[string(types.QueueAttributeNameQueueArn)], nil
}

// allowRules lets the given rules, and nothing else, deliver to the queue.
func allowRules(ctx context.Context, client *sqs.Client, queueURL, queueARN string, ruleARNs []string) error {
	policy, err := queuePolicy(queueARN, ruleARNs)
	if err != nil {
		return err
	}
	_, err = client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(queueURL),
		Attributes: map[string]string{"Policy": policy},
	})
	if err != nil {
		return fmt.Errorf("set SQS queue policy: %w", err)
	}
	return nil
}

func queuePolicy(queueARN string, ruleARNs []string) (string, error) {
	policy, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":       "AllowEmailServiceRules",
			"Effect":    "Allow",
			"Principal": map[string]string{"Service": "events.amazonaws.com"},
			"Action":    "sqs:SendMessage",
			"Resource":  queueARN,
			"Condition": map[string]any{
				"ArnEquals": map[string][]string{"aws:SourceArn": ruleARNs},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("encode SQS queue policy: %w", err)
	}
	return string(policy), nil
}
