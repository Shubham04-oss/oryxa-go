package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type presignAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*presignedGet, error)
}

type presignedGet struct {
	URL string
}

type awsPresigner struct {
	inner *s3.PresignClient
}

func (presigner awsPresigner) PresignGetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*presignedGet, error) {
	out, err := presigner.inner.PresignGetObject(ctx, input, optFns...)
	if err != nil {
		return nil, err
	}
	return &presignedGet{URL: out.URL}, nil
}

// S3 is the production object store. The files service is the only principal
// that talks to the bucket; callers never receive AWS credentials.
type S3 struct {
	client  s3API
	presign presignAPI
	bucket  string
}

func NewS3(client *s3.Client, bucket string) *S3 {
	return &S3{client: client, presign: awsPresigner{inner: s3.NewPresignClient(client)}, bucket: bucket}
}

func NewS3FromConfig(awsConfig aws.Config, endpoint, bucket string) *S3 {
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if strings.TrimSpace(endpoint) != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return NewS3(client, bucket)
}

func (store *S3) Put(ctx context.Context, object Object) error {
	if store.bucket == "" || object.Key == "" {
		return fmt.Errorf("bucket and object key are required: %w", domain.ErrInvalid)
	}
	_, err := store.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(store.bucket),
		Key:                  aws.String(object.Key),
		Body:                 bytes.NewReader(object.Body),
		ContentLength:        aws.Int64(int64(len(object.Body))),
		ContentType:          aws.String(object.ContentType),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
		Metadata: map[string]string{
			"oryxa-sha256": object.SHA256,
		},
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (store *S3) Get(ctx context.Context, key string) (Object, error) {
	output, err := store.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return Object{}, domain.ErrNotFound
		}
		return Object{}, fmt.Errorf("get object: %w", err)
	}
	defer output.Body.Close()
	body, err := io.ReadAll(io.LimitReader(output.Body, domain.MaxBytes+1))
	if err != nil {
		return Object{}, fmt.Errorf("read object: %w", err)
	}
	contentType := "application/octet-stream"
	if output.ContentType != nil && *output.ContentType != "" {
		contentType = *output.ContentType
	}
	return Object{Key: key, ContentType: contentType, Body: body}, nil
}

func (store *S3) Delete(ctx context.Context, key string) error {
	if _, err := store.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.bucket), Key: aws.String(key)}); err != nil {
		if isNotFound(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("head object: %w", err)
	}
	if _, err := store.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(store.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (store *S3) DeletePrefix(ctx context.Context, prefix string) error {
	var token *string
	for {
		listed, err := store.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(store.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}
		if len(listed.Contents) == 0 {
			if aws.ToBool(listed.IsTruncated) && listed.NextContinuationToken != nil {
				token = listed.NextContinuationToken
				continue
			}
			return nil
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(listed.Contents))
		for _, item := range listed.Contents {
			objects = append(objects, s3types.ObjectIdentifier{Key: item.Key})
		}
		if _, err := store.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(store.bucket),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
		if !aws.ToBool(listed.IsTruncated) {
			return nil
		}
		token = listed.NextContinuationToken
	}
}

func (store *S3) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if expiry <= 0 {
		expiry = time.Minute
	}
	if _, err := store.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.bucket), Key: aws.String(key)}); err != nil {
		if isNotFound(err) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("head object: %w", err)
	}
	out, err := store.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return out.URL, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var notFound *s3types.NotFound
	var noSuchKey *s3types.NoSuchKey
	return errors.As(err, &notFound) || errors.As(err, &noSuchKey)
}
