// Package originals keeps the bytes of a document knowledge does not read
// itself — a PDF, an Office file — in S3, when KNOWLEDGE_ORIGINALS_BUCKET
// says there is somewhere to put them. Everything else about such a document
// still goes through the ordinary path: it is application.Originals, the
// seam sync.go reads through, that knows nothing here exists.
package originals

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3API is the surface this package needs of S3, so a test answers without a
// network.
type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3 is knowledge's own bucket for document originals. Nothing outside
// knowledge ever reads or writes it directly: a consumer reads the object
// named in Original.Bucket and Original.Key from the published event, with
// its own credentials for that bucket.
type S3 struct {
	client s3API
	bucket string
}

// NewS3 builds an S3-backed Originals on an already-configured client and
// bucket. The host builds the client; this package places no credential of
// its own.
func NewS3(client *s3.Client, bucket string) *S3 {
	return &S3{client: client, bucket: bucket}
}

// NewS3FromConfig builds the production client, pointed at endpoint when set
// (a local emulator) rather than AWS itself.
func NewS3FromConfig(awsConfig aws.Config, endpoint, bucket string) *S3 {
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return NewS3(client, bucket)
}

// Put writes content under key, replacing whatever was there. Server-side
// encryption matches what files' own object store already does for the
// organization's uploads: this bucket holds the same sort of thing.
func (store *S3) Put(ctx context.Context, key string, content []byte, mime string) error {
	_, err := store.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(store.bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(content),
		ContentLength:        aws.Int64(int64(len(content))),
		ContentType:          aws.String(mime),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("put original: %w", err)
	}
	return nil
}

// Delete removes an object a document no longer needs. Deleting a key that
// is already gone is not an error to S3 itself, and this package does not
// make it one: the end state — nothing at that key — is what a caller wants.
func (store *S3) Delete(ctx context.Context, key string) error {
	if _, err := store.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete original: %w", err)
	}
	return nil
}
