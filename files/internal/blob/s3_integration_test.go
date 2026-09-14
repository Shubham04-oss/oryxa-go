package blob

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

func TestMemoryRoundTrip(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	if err := store.Put(ctx, Object{Key: "org/org_1/obj/file_1", ContentType: "text/plain", SHA256: "ab", Body: []byte("hello")}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, "org/org_1/obj/file_1")
	if err != nil || string(got.Body) != "hello" {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	url, err := store.PresignGet(ctx, "org/org_1/obj/file_1", time.Minute)
	if err != nil || url != "memory://org/org_1/obj/file_1" {
		t.Fatalf("PresignGet() = %q, %v", url, err)
	}
	if err := store.DeletePrefix(ctx, "org/org_1/"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "org/org_1/obj/file_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get after prefix delete = %v", err)
	}
}

func TestS3RoundTrip(t *testing.T) {
	endpoint := os.Getenv("FILES_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("FILES_TEST_AWS_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(config, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	bucket := "oryxa-files-it-" + time.Now().UTC().Format("20060102150405000000000")
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraintApSouth1,
		},
	})
	if err != nil {
		t.Fatalf("CreateBucket() error = %v", err)
	}
	defer func() {
		store := NewS3(client, bucket)
		_ = store.DeletePrefix(context.Background(), "")
		_, _ = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	}()

	store := NewS3(client, bucket)
	key := domain.Object{OrganizationID: "org_1", ID: "file_1", Scope: domain.ScopeMember, MemberID: "user-1", CreatedAt: time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)}.Key()
	if err := store.Put(ctx, Object{Key: key, ContentType: "text/plain", SHA256: "aa", Body: []byte("hello")}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || !bytes.Equal(got.Body, []byte("hello")) {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	url, err := store.PresignGet(ctx, key, time.Minute)
	if err != nil || url == "" {
		t.Fatalf("PresignGet() = %q, %v", url, err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
}
