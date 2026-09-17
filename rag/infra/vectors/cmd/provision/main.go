// Command provision creates the AWS resources rag owns, and is repeatable.
//
// It prints the queue URL it created; put that in RAG_EVENTS_QUEUE_URL.
// LocalStack forgets buses, rules, queues and buckets when it restarts, so run
// this again after one.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3vectors"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/rag/infra/vectors/internal/provision"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config := provision.FromEnv()
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.Region))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	endpoint := strings.TrimRight(firstNonEmpty(os.Getenv("AWS_ENDPOINT_URL_S3VECTORS"), os.Getenv("AWS_ENDPOINT_URL")), "/")
	vectorsClient := s3vectors.NewFromConfig(awsConfig, func(options *s3vectors.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	})
	output, err := provision.Run(ctx, provision.Clients{
		EventBridge: eventbridge.NewFromConfig(awsConfig),
		SQS:         sqs.NewFromConfig(awsConfig),
		S3Vectors:   vectorsClient,
	}, config)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		log.Fatalf("write provision output: %v", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
