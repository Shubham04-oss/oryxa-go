// Command provision creates the bucket agents primary keeps ADK artifacts
// in: private, encrypted, and expiring on its own so a conversation's
// leftovers do not accumulate forever. It is repeatable — a bucket that
// already exists is left where it is and its settings are reapplied.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// expireDays is how long an artifact lives before S3 removes it. Artifacts
// belong to a conversation, and a conversation is not an archive.
const expireDays = 30

// abortMultipartDays drops uploads nobody finished.
const abortMultipartDays = 1

// recoverableDeleteDays is how long an artifact that was deleted or replaced
// stays recoverable. OpenTofu versions the bucket
// (deploy/production/hardening.tf); S3 removes an older version this many days
// after it stopped being the newest, and then the delete marker left behind.
const recoverableDeleteDays = 30

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bucket := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_ARTIFACT_BUCKET"))
	if bucket == "" {
		log.Fatal("AGENTS_PRIMARY_ARTIFACT_BUCKET is required")
	}
	region := valueOr(os.Getenv("AWS_REGION"), "ap-south-1")

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	endpoint := strings.TrimRight(valueOr(os.Getenv("AWS_ENDPOINT_URL_S3"), os.Getenv("AWS_ENDPOINT_URL")), "/")
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})

	if err := ensureBucket(ctx, client, bucket, region); err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"region":      region,
		"bucket_name": bucket,
		"expire_days": expireDays,
	}); err != nil {
		log.Fatalf("write provision output: %v", err)
	}
}

func ensureBucket(ctx context.Context, client *s3.Client, bucket, region string) error {
	input := &s3.CreateBucketInput{
		Bucket:          aws.String(bucket),
		ObjectOwnership: s3types.ObjectOwnershipBucketOwnerEnforced,
	}
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(region),
		}
	}
	if _, err := client.CreateBucket(ctx, input); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if !errors.As(err, &owned) && !errors.As(err, &exists) &&
			!strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
			return fmt.Errorf("create bucket %s: %w", bucket, err)
		}
	}
	if _, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("block public access on %s: %w", bucket, err)
	}
	if _, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
			Rules: []s3types.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
					SSEAlgorithm: s3types.ServerSideEncryptionAes256,
				},
				BucketKeyEnabled: aws.Bool(true),
			}},
		},
	}); err != nil {
		return fmt.Errorf("encrypt bucket %s: %w", bucket, err)
	}
	if _, err := client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{{
				ID:         aws.String("expire-artifacts"),
				Status:     s3types.ExpirationStatusEnabled,
				Filter:     &s3types.LifecycleRuleFilter{},
				Expiration: &s3types.LifecycleExpiration{Days: aws.Int32(expireDays)},
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
		return fmt.Errorf("lifecycle on %s: %w", bucket, err)
	}
	if _, err := client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(bucket),
		Tagging: &s3types.Tagging{TagSet: []s3types.Tag{
			{Key: aws.String("application"), Value: aws.String("oryxa")},
			{Key: aws.String("service"), Value: aws.String("agents-primary")},
		}},
	}); err != nil {
		return fmt.Errorf("tag bucket %s: %w", bucket, err)
	}
	log.Printf("bucket %s ready in %s, artifacts expire after %d days", bucket, region, expireDays)
	return nil
}

func valueOr(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}
