// Package s3artifact stores ADK artifacts in an S3 bucket, so what a
// conversation produces — a screenshot, a file the sandbox made, anything
// save_artifact keeps — outlives the process that made it.
//
// The key layout is adk-go's own, the one artifact/gcsartifact uses, so an
// artifact written here is the same artifact read anywhere else:
//
//	{appName}/{userID}/{sessionID}/{fileName}/{version}
//	{appName}/{userID}/user/{fileName}/{version}       // a "user:" filename
//
// A filename beginning with "user:" belongs to the member rather than to one
// conversation, and every session of theirs sees it.
//
// Oryxa reaches it through ByOrganization, which puts the organization in
// front of the app name, so every key starts orgs/{organizationID}/.
package s3artifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
)

const (
	// maxSaveAttempts caps Save's retries. Each retry means another writer took
	// the version this one picked, so it is how many colliding writers are
	// tolerated before ErrVersionConflict.
	maxSaveAttempts = 16
	// saveRetryBaseDelay and saveRetryMaxDelay bound the jittered backoff
	// between those retries.
	saveRetryBaseDelay = 10 * time.Millisecond
	saveRetryMaxDelay  = time.Second

	defaultMIMEType = "application/octet-stream"

	// MaxArtifactBytes is the most one artifact version may hold, written or
	// read. Load holds a whole artifact in memory, so without a bound one
	// oversized object could take the process down. The largest artifact
	// Oryxa writes is a document edited in Docs, which the documents route
	// caps at 50 MiB; screenshots stop at 10 MiB and sandbox files at what one
	// read moves. 64 MiB keeps every one of those with room to spare.
	MaxArtifactBytes = 64 << 20
)

// ErrVersionConflict reports that Save could not claim a version within its
// retry budget because concurrent writers kept taking the one it picked. It
// is always wrapped, so test for it with errors.Is; the save can be retried.
var ErrVersionConflict = errors.New("artifact version conflict")

// ErrTooLarge reports an artifact over MaxArtifactBytes: a save is refused
// before anything is written, and a load before the object is read into
// memory. It is always wrapped, so test for it with errors.Is.
var ErrTooLarge = errors.New("artifact too large")

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// Service is the S3 artifact.Service.
type Service struct {
	client s3API
	bucket string
	// prefix is put before every key, so a bucket shared with other data
	// (R2's oryxa-runs) keeps artifacts apart; empty on AWS.
	prefix string
	// encrypt asks S3 for SSE-S3 on every write. R2 encrypts everything at
	// rest and has no such header, so it is off there.
	encrypt bool
	// sleep waits for d or until ctx is done; a field so tests can stub out
	// the Save retry backoff. Never nil.
	sleep func(ctx context.Context, d time.Duration) error
}

// New returns a Service storing artifacts in bucket.
func New(client *s3.Client, bucket string) (*Service, error) {
	return newService(client, bucket)
}

// NewFromConfig builds the S3 client itself. A non-empty endpoint overrides
// AWS, for the LocalStack emulator in docker-compose.yml.
func NewFromConfig(awsConfig aws.Config, endpoint, bucket string) (*Service, error) {
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if strings.TrimSpace(endpoint) != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return newService(client, bucket)
}

func newService(client s3API, bucket string) (*Service, error) {
	if client == nil {
		return nil, errors.New("s3artifact: an S3 client is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("s3artifact: a bucket is required")
	}
	return &Service{client: client, bucket: bucket, encrypt: true, sleep: sleepContext}, nil
}

// R2 reaches a Cloudflare R2 bucket through its S3 API (live-layer A7).
type R2 struct {
	// Endpoint is https://{account}.r2.cloudflarestorage.com.
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	// Prefix is put before every key, such as "artifacts/".
	Prefix string
}

// NewR2 returns a Service storing artifacts in an R2 bucket, keyed as on S3
// under r2.Prefix.
func NewR2(awsConfig aws.Config, r2 R2) (*Service, error) {
	if strings.TrimSpace(r2.Endpoint) == "" || r2.AccessKeyID == "" || r2.SecretAccessKey == "" {
		return nil, errors.New("s3artifact: R2 needs an endpoint and an access key")
	}
	if r2.Prefix != "" && !strings.HasSuffix(r2.Prefix, "/") {
		return nil, errors.New("s3artifact: an R2 prefix must end in /")
	}
	config := awsConfig.Copy()
	config.Region = "auto"
	config.Credentials = credentials.NewStaticCredentialsProvider(r2.AccessKeyID, r2.SecretAccessKey, "")
	client := s3.NewFromConfig(config, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(strings.TrimRight(r2.Endpoint, "/"))
		options.UsePathStyle = true
		// R2 checks no request checksum, and refuses the ones the SDK adds
		// by default on some operations.
		options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		options.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	service, err := newService(client, r2.Bucket)
	if err != nil {
		return nil, err
	}
	service.prefix = r2.Prefix
	service.encrypt = false
	return service, nil
}

// Save implements artifact.Service.
//
// S3 cannot list and write atomically, so versions are assigned
// optimistically: Save writes max+1 with an If-None-Match precondition and,
// when another writer already took that version, backs off and retries with a
// fresh one. It never overwrites a version that exists.
func (service *Service) Save(ctx context.Context, request *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}
	content, mimeType := partBytes(request.Part)
	if len(content) > MaxArtifactBytes {
		return nil, fmt.Errorf("save artifact %q: %d bytes is over the %d-byte limit: %w",
			request.FileName, len(content), MaxArtifactBytes, ErrTooLarge)
	}

	var lastErr error
	for attempt := range maxSaveAttempts {
		version := request.Version
		if version == 0 {
			existing, err := service.versions(ctx, request.AppName, request.UserID, request.SessionID, request.FileName)
			if err != nil {
				return nil, fmt.Errorf("list artifact versions: %w", err)
			}
			version = 1
			if len(existing) > 0 {
				version = slices.Max(existing) + 1
			}
		}

		key := service.key(request.AppName, request.UserID, request.SessionID, request.FileName, version)
		input := &s3.PutObjectInput{
			Bucket:        aws.String(service.bucket),
			Key:           aws.String(key),
			Body:          bytes.NewReader(content),
			ContentLength: aws.Int64(int64(len(content))),
			ContentType:   aws.String(mimeType),
			IfNoneMatch:   aws.String("*"),
		}
		if service.encrypt {
			input.ServerSideEncryption = s3types.ServerSideEncryptionAes256
		}
		_, err := service.client.PutObject(ctx, input)
		if err == nil {
			return &artifact.SaveResponse{Version: version}, nil
		}
		if !isPreconditionFailed(err) {
			return nil, fmt.Errorf("save artifact: %w", err)
		}
		// An explicit version that is already taken is the caller's answer,
		// not something a retry can resolve.
		if request.Version != 0 {
			return nil, fmt.Errorf("save artifact %q version %d: %w", request.FileName, request.Version, ErrVersionConflict)
		}
		lastErr = err
		if attempt < maxSaveAttempts-1 {
			if err := service.sleep(ctx, backoffDelay(attempt)); err != nil {
				return nil, fmt.Errorf("save artifact %q: %w", request.FileName, err)
			}
		}
	}
	return nil, fmt.Errorf("save artifact %q after %d attempts (last error: %v): %w",
		request.FileName, maxSaveAttempts, lastErr, ErrVersionConflict)
}

// Load implements artifact.Service.
func (service *Service) Load(ctx context.Context, request *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}
	version, err := service.resolveVersion(ctx, request.AppName, request.UserID, request.SessionID, request.FileName, request.Version)
	if err != nil {
		return nil, err
	}
	key := service.key(request.AppName, request.UserID, request.SessionID, request.FileName, version)

	output, err := service.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(service.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("artifact %q not found: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("get artifact: %w", err)
	}
	defer output.Body.Close()
	// The declared length refuses an oversized object without reading it; the
	// limited read refuses one whose length was not declared, or was wrong.
	if length := aws.ToInt64(output.ContentLength); length > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact %q: %d bytes is over the %d-byte limit: %w", key, length, MaxArtifactBytes, ErrTooLarge)
	}
	content, err := io.ReadAll(io.LimitReader(output.Body, MaxArtifactBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", key, err)
	}
	if len(content) > MaxArtifactBytes {
		return nil, fmt.Errorf("artifact %q is over the %d-byte limit: %w", key, MaxArtifactBytes, ErrTooLarge)
	}
	mimeType := defaultMIMEType
	if output.ContentType != nil && *output.ContentType != "" {
		mimeType = *output.ContentType
	}
	return &artifact.LoadResponse{Part: genai.NewPartFromBytes(content, mimeType)}, nil
}

// Delete implements artifact.Service. Deleting what is not there is not an
// error.
func (service *Service) Delete(ctx context.Context, request *artifact.DeleteRequest) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("request validation failed: %w", err)
	}
	if request.Version != 0 {
		key := service.key(request.AppName, request.UserID, request.SessionID, request.FileName, request.Version)
		if _, err := service.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(service.bucket),
			Key:    aws.String(key),
		}); err != nil {
			return fmt.Errorf("delete artifact: %w", err)
		}
		return nil
	}

	versions, err := service.versions(ctx, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return fmt.Errorf("list artifact versions: %w", err)
	}
	if len(versions) == 0 {
		return nil
	}
	objects := make([]s3types.ObjectIdentifier, 0, len(versions))
	for _, version := range versions {
		objects = append(objects, s3types.ObjectIdentifier{
			Key: aws.String(service.key(request.AppName, request.UserID, request.SessionID, request.FileName, version)),
		})
	}
	if _, err := service.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(service.bucket),
		Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
	}); err != nil {
		return fmt.Errorf("delete artifact versions: %w", err)
	}
	return nil
}

// List implements artifact.Service: the session's artifacts and the member's
// own, by name.
func (service *Service) List(ctx context.Context, request *artifact.ListRequest) (*artifact.ListResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}
	names := map[string]bool{}
	for _, prefix := range []string{
		service.prefix + buildSessionPrefix(request.AppName, request.UserID, request.SessionID),
		service.prefix + buildUserPrefix(request.AppName, request.UserID),
	} {
		keys, err := service.keys(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("list artifacts: %w", err)
		}
		for _, key := range keys {
			// .../{fileName}/{version}
			segments := strings.Split(key, "/")
			if len(segments) < 2 {
				continue
			}
			names[segments[len(segments)-2]] = true
		}
	}
	listed := slices.Collect(maps.Keys(names))
	sort.Strings(listed)
	return &artifact.ListResponse{FileNames: listed}, nil
}

// Versions implements artifact.Service, and reports fs.ErrNotExist when the
// artifact has none.
func (service *Service) Versions(ctx context.Context, request *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}
	versions, err := service.versions(ctx, request.AppName, request.UserID, request.SessionID, request.FileName)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("artifact %q not found: %w", request.FileName, fs.ErrNotExist)
	}
	return &artifact.VersionsResponse{Versions: versions}, nil
}

// GetArtifactVersion implements artifact.Service.
func (service *Service) GetArtifactVersion(ctx context.Context, request *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	if err := request.Validate(); err != nil {
		return nil, fmt.Errorf("request validation failed: %w", err)
	}
	version, err := service.resolveVersion(ctx, request.AppName, request.UserID, request.SessionID, request.FileName, request.Version)
	if err != nil {
		return nil, err
	}
	key := service.key(request.AppName, request.UserID, request.SessionID, request.FileName, version)

	head, err := service.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(service.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("artifact %q not found: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("head artifact: %w", err)
	}
	metadata := make(map[string]any, len(head.Metadata))
	for name, value := range head.Metadata {
		metadata[name] = value
	}
	mimeType := defaultMIMEType
	if head.ContentType != nil && *head.ContentType != "" {
		mimeType = *head.ContentType
	}
	return &artifact.GetArtifactVersionResponse{
		ArtifactVersion: &artifact.ArtifactVersion{
			Version: version,
			// The s3:// form identifies the object in the scheme native to
			// this store, as the interface asks; it is not a download URL.
			CanonicalURI:   fmt.Sprintf("s3://%s/%s", service.bucket, key),
			CustomMetadata: metadata,
			CreateTime:     aws.ToTime(head.LastModified),
			MimeType:       mimeType,
		},
	}, nil
}

// versions reports the versions an artifact has, newest last, and no error
// when it has none.
func (service *Service) versions(ctx context.Context, appName, userID, sessionID, fileName string) ([]int64, error) {
	keys, err := service.keys(ctx, service.prefix+buildKeyPrefix(appName, userID, sessionID, fileName))
	if err != nil {
		return nil, err
	}
	versions := make([]int64, 0, len(keys))
	for _, key := range keys {
		segments := strings.Split(key, "/")
		version, err := strconv.ParseInt(segments[len(segments)-1], 10, 64)
		// A key whose last segment is not a number is not a version of this
		// artifact; leave it alone rather than failing the listing.
		if err != nil {
			continue
		}
		versions = append(versions, version)
	}
	slices.Sort(versions)
	return versions, nil
}

func (service *Service) keys(ctx context.Context, prefix string) ([]string, error) {
	var (
		keys  []string
		token *string
	)
	for {
		listed, err := service.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(service.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		for _, item := range listed.Contents {
			keys = append(keys, aws.ToString(item.Key))
		}
		if !aws.ToBool(listed.IsTruncated) || listed.NextContinuationToken == nil {
			return keys, nil
		}
		token = listed.NextContinuationToken
	}
}

// resolveVersion answers version when it is set, and the newest otherwise.
func (service *Service) resolveVersion(ctx context.Context, appName, userID, sessionID, fileName string, version int64) (int64, error) {
	if version != 0 {
		return version, nil
	}
	versions, err := service.versions(ctx, appName, userID, sessionID, fileName)
	if err != nil {
		return 0, fmt.Errorf("list artifact versions: %w", err)
	}
	if len(versions) == 0 {
		return 0, fmt.Errorf("artifact %q not found: %w", fileName, fs.ErrNotExist)
	}
	return slices.Max(versions), nil
}

// partBytes is what to store for a part, and the type to store it as. ADK
// validates that one of the two is set.
func partBytes(part *genai.Part) ([]byte, string) {
	if part.InlineData != nil {
		mimeType := part.InlineData.MIMEType
		if mimeType == "" {
			mimeType = defaultMIMEType
		}
		return part.InlineData.Data, mimeType
	}
	return []byte(part.Text), "text/plain"
}

// userNamespaced reports whether a filename belongs to the member rather than
// to one conversation, which adk-go marks with a "user:" prefix.
func userNamespaced(fileName string) bool {
	return strings.HasPrefix(fileName, "user:")
}

// key is a version's object key, under the service's prefix.
func (service *Service) key(appName, userID, sessionID, fileName string, version int64) string {
	return service.prefix + buildKey(appName, userID, sessionID, fileName, version)
}

func buildKey(appName, userID, sessionID, fileName string, version int64) string {
	return fmt.Sprintf("%s%d", buildKeyPrefix(appName, userID, sessionID, fileName), version)
}

func buildKeyPrefix(appName, userID, sessionID, fileName string) string {
	if userNamespaced(fileName) {
		return fmt.Sprintf("%s/%s/user/%s/", appName, userID, fileName)
	}
	return fmt.Sprintf("%s/%s/%s/%s/", appName, userID, sessionID, fileName)
}

func buildSessionPrefix(appName, userID, sessionID string) string {
	return fmt.Sprintf("%s/%s/%s/", appName, userID, sessionID)
}

func buildUserPrefix(appName, userID string) string {
	return fmt.Sprintf("%s/%s/user/", appName, userID)
}

// backoffDelay is a full-jitter backoff for a zero-based retry attempt.
func backoffDelay(attempt int) time.Duration {
	delay := saveRetryMaxDelay
	if attempt < 63 {
		if scaled := saveRetryBaseDelay << attempt; scaled > 0 && scaled < delay {
			delay = scaled
		}
	}
	return time.Duration(rand.Int64N(int64(delay) + 1))
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isPreconditionFailed reports whether err is S3 refusing a write because the
// object already exists: the If-None-Match precondition this package puts on
// every save.
func isPreconditionFailed(err error) bool {
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) && response.HTTPStatusCode() == http.StatusPreconditionFailed {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "PreconditionFailed"
	}
	return false
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var notFound *s3types.NotFound
	var noSuchKey *s3types.NoSuchKey
	return errors.As(err, &notFound) || errors.As(err, &noSuchKey)
}

var _ artifact.Service = (*Service)(nil)
