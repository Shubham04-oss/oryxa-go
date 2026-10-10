package s3artifact

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
)

// fakeS3 is an in-memory bucket that honours the If-None-Match precondition
// Save relies on.
type fakeS3 struct {
	objects map[string]fakeObject
	// rejectPuts fails that many PutObject calls with a precondition error
	// before behaving, standing in for another writer taking the version.
	rejectPuts int
	puts       int
	// streamed are objects served from a generated body instead of objects,
	// so a test can offer one larger than it wants to hold in memory.
	streamed map[string]streamedObject
}

// streamedObject is size zero bytes, with ContentLength declared or not, as
// S3 omits it on some responses.
type streamedObject struct {
	size     int64
	declared bool
}

type fakeObject struct {
	body        []byte
	contentType string
	modified    time.Time
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string]fakeObject{}}
}

func (fake *fakeS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	fake.puts++
	if fake.rejectPuts > 0 {
		fake.rejectPuts--
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object already exists"}
	}
	key := aws.ToString(input.Key)
	if _, taken := fake.objects[key]; taken && aws.ToString(input.IfNoneMatch) == "*" {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "object already exists"}
	}
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	fake.objects[key] = fakeObject{
		body:        body,
		contentType: aws.ToString(input.ContentType),
		modified:    time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC),
	}
	return &s3.PutObjectOutput{}, nil
}

func (fake *fakeS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if stream, ok := fake.streamed[aws.ToString(input.Key)]; ok {
		output := &s3.GetObjectOutput{
			Body:        io.NopCloser(io.LimitReader(zeros{}, stream.size)),
			ContentType: aws.String("application/octet-stream"),
		}
		if stream.declared {
			output.ContentLength = aws.Int64(stream.size)
		}
		return output, nil
	}
	object, ok := fake.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{
		Body:        io.NopCloser(strings.NewReader(string(object.body))),
		ContentType: aws.String(object.contentType),
	}, nil
}

func (fake *fakeS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(fake.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (fake *fakeS3) DeleteObjects(_ context.Context, input *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, object := range input.Delete.Objects {
		delete(fake.objects, aws.ToString(object.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (fake *fakeS3) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(input.Prefix)
	var contents []s3types.Object
	for key := range fake.objects {
		if strings.HasPrefix(key, prefix) {
			contents = append(contents, s3types.Object{Key: aws.String(key)})
		}
	}
	slices.SortFunc(contents, func(a, b s3types.Object) int {
		return strings.Compare(aws.ToString(a.Key), aws.ToString(b.Key))
	})
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}

func (fake *fakeS3) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	object, ok := fake.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentType:  aws.String(object.contentType),
		LastModified: aws.Time(object.modified),
		Metadata:     map[string]string{"origin": "test"},
	}, nil
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func newTestService(t *testing.T, fake *fakeS3) *Service {
	t.Helper()
	service, err := newService(fake, "artifacts")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	// No real waiting in the retry path.
	service.sleep = func(context.Context, time.Duration) error { return nil }
	return service
}

func save(t *testing.T, service *Service, sessionID, fileName, body string) int64 {
	t.Helper()
	saved, err := service.Save(context.Background(), &artifact.SaveRequest{
		AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: fileName,
		Part: genai.NewPartFromBytes([]byte(body), "text/plain"),
	})
	if err != nil {
		t.Fatalf("save %s: %v", fileName, err)
	}
	return saved.Version
}

func TestSaveAssignsIncrementingVersions(t *testing.T) {
	fake := newFakeS3()
	service := newTestService(t, fake)

	for want := int64(1); want <= 3; want++ {
		if got := save(t, service, "session-1", "notes.txt", "body"); got != want {
			t.Fatalf("version %d, want %d", got, want)
		}
	}
	if _, ok := fake.objects["krude/user-owner/session-1/notes.txt/3"]; !ok {
		t.Fatalf("expected key krude/user-owner/session-1/notes.txt/3, have %v", objectKeys(fake.objects))
	}
}

func TestSaveUserNamespacedArtifactIsSharedAcrossSessions(t *testing.T) {
	fake := newFakeS3()
	service := newTestService(t, fake)

	save(t, service, "session-1", "user:profile.json", "{}")
	if _, ok := fake.objects["krude/user-owner/user/user:profile.json/1"]; !ok {
		t.Fatalf("expected a user-namespaced key, have %v", objectKeys(fake.objects))
	}
	// A second session saving the same name continues the same history.
	if version := save(t, service, "session-2", "user:profile.json", "{}"); version != 2 {
		t.Fatalf("version %d, want 2", version)
	}
}

func TestLoadReturnsLatestThenSpecificVersion(t *testing.T) {
	service := newTestService(t, newFakeS3())
	save(t, service, "session-1", "notes.txt", "first")
	save(t, service, "session-1", "notes.txt", "second")

	latest, err := service.Load(context.Background(), &artifact.LoadRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
	})
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if got := string(latest.Part.InlineData.Data); got != "second" {
		t.Fatalf("latest is %q, want %q", got, "second")
	}
	if got := latest.Part.InlineData.MIMEType; got != "text/plain" {
		t.Fatalf("mime type is %q, want text/plain", got)
	}

	first, err := service.Load(context.Background(), &artifact.LoadRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt", Version: 1,
	})
	if err != nil {
		t.Fatalf("load version 1: %v", err)
	}
	if got := string(first.Part.InlineData.Data); got != "first" {
		t.Fatalf("version 1 is %q, want %q", got, "first")
	}
}

func TestSaveRefusesAnArtifactOverTheLimit(t *testing.T) {
	fake := newFakeS3()
	service := newTestService(t, fake)

	_, err := service.Save(context.Background(), &artifact.SaveRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "huge.bin",
		Part: genai.NewPartFromBytes(make([]byte, MaxArtifactBytes+1), "application/octet-stream"),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error is %v, want ErrTooLarge", err)
	}
	if fake.puts != 0 || len(fake.objects) != 0 {
		t.Fatalf("an oversized save wrote %d time(s): %v", fake.puts, objectKeys(fake.objects))
	}
}

func TestLoadRefusesAnArtifactOverTheLimit(t *testing.T) {
	load := func(service *Service) error {
		_, err := service.Load(context.Background(), &artifact.LoadRequest{
			AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "huge.bin", Version: 1,
		})
		return err
	}
	const key = "krude/user-owner/session-1/huge.bin/1"
	for name, stream := range map[string]streamedObject{
		// Refused from its declared length, before a byte is read.
		"declared length": {size: MaxArtifactBytes + 1, declared: true},
		// No length to go by: the bounded read stops one byte past the limit.
		"undeclared length": {size: MaxArtifactBytes + 1},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeS3()
			fake.streamed = map[string]streamedObject{key: stream}
			if err := load(newTestService(t, fake)); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("error is %v, want ErrTooLarge", err)
			}
		})
	}

	t.Run("exactly at the limit", func(t *testing.T) {
		fake := newFakeS3()
		fake.streamed = map[string]streamedObject{key: {size: MaxArtifactBytes}}
		if err := load(newTestService(t, fake)); err != nil {
			t.Fatalf("an artifact of exactly MaxArtifactBytes failed to load: %v", err)
		}
	})
}

func TestLoadMissingArtifactIsNotExist(t *testing.T) {
	service := newTestService(t, newFakeS3())
	_, err := service.Load(context.Background(), &artifact.LoadRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "absent.txt",
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error is %v, want fs.ErrNotExist", err)
	}
}

func TestListCombinesSessionAndUserArtifacts(t *testing.T) {
	service := newTestService(t, newFakeS3())
	save(t, service, "session-1", "notes.txt", "a")
	save(t, service, "session-1", "chart.png", "b")
	save(t, service, "session-1", "user:profile.json", "{}")
	// Another session's artifact stays out of this listing.
	save(t, service, "session-2", "elsewhere.txt", "c")

	listed, err := service.List(context.Background(), &artifact.ListRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"chart.png", "notes.txt", "user:profile.json"}
	if !slices.Equal(listed.FileNames, want) {
		t.Fatalf("listed %v, want %v", listed.FileNames, want)
	}
}

func TestVersionsMissingArtifactIsNotExist(t *testing.T) {
	service := newTestService(t, newFakeS3())
	_, err := service.Versions(context.Background(), &artifact.VersionsRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "absent.txt",
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error is %v, want fs.ErrNotExist", err)
	}
}

func TestDeleteRemovesOneVersionThenEveryVersion(t *testing.T) {
	fake := newFakeS3()
	service := newTestService(t, fake)
	save(t, service, "session-1", "notes.txt", "first")
	save(t, service, "session-1", "notes.txt", "second")

	if err := service.Delete(context.Background(), &artifact.DeleteRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt", Version: 1,
	}); err != nil {
		t.Fatalf("delete version 1: %v", err)
	}
	versions, err := service.Versions(context.Background(), &artifact.VersionsRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
	})
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if !slices.Equal(versions.Versions, []int64{2}) {
		t.Fatalf("versions %v, want [2]", versions.Versions)
	}

	if err := service.Delete(context.Background(), &artifact.DeleteRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
	}); err != nil {
		t.Fatalf("delete every version: %v", err)
	}
	if len(fake.objects) != 0 {
		t.Fatalf("objects remain: %v", objectKeys(fake.objects))
	}
}

func TestDeleteMissingArtifactIsNotAnError(t *testing.T) {
	service := newTestService(t, newFakeS3())
	if err := service.Delete(context.Background(), &artifact.DeleteRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "absent.txt",
	}); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
}

func TestSaveRetriesWhenAnotherWriterTakesTheVersion(t *testing.T) {
	fake := newFakeS3()
	fake.rejectPuts = 2
	service := newTestService(t, fake)

	if version := save(t, service, "session-1", "notes.txt", "body"); version != 1 {
		t.Fatalf("version %d, want 1", version)
	}
	if fake.puts != 3 {
		t.Fatalf("%d puts, want 3 (two refused, one stored)", fake.puts)
	}
}

func TestSaveGivesUpAfterTooManyConflicts(t *testing.T) {
	fake := newFakeS3()
	fake.rejectPuts = maxSaveAttempts + 1
	service := newTestService(t, fake)

	_, err := service.Save(context.Background(), &artifact.SaveRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
		Part: genai.NewPartFromBytes([]byte("body"), "text/plain"),
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error is %v, want ErrVersionConflict", err)
	}
}

func TestSaveWithAnExplicitTakenVersionDoesNotRetry(t *testing.T) {
	fake := newFakeS3()
	service := newTestService(t, fake)
	save(t, service, "session-1", "notes.txt", "first")
	before := fake.puts

	_, err := service.Save(context.Background(), &artifact.SaveRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
		Part: genai.NewPartFromBytes([]byte("second"), "text/plain"), Version: 1,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error is %v, want ErrVersionConflict", err)
	}
	if fake.puts != before+1 {
		t.Fatalf("%d puts after the conflict, want %d: an explicit version must not be retried", fake.puts-before, 1)
	}
}

func TestGetArtifactVersionDescribesTheStoredObject(t *testing.T) {
	service := newTestService(t, newFakeS3())
	save(t, service, "session-1", "notes.txt", "body")

	described, err := service.GetArtifactVersion(context.Background(), &artifact.GetArtifactVersionRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-1", FileName: "notes.txt",
	})
	if err != nil {
		t.Fatalf("get artifact version: %v", err)
	}
	version := described.ArtifactVersion
	if version.Version != 1 {
		t.Fatalf("version %d, want 1", version.Version)
	}
	if want := "s3://artifacts/krude/user-owner/session-1/notes.txt/1"; version.CanonicalURI != want {
		t.Fatalf("canonical URI %q, want %q", version.CanonicalURI, want)
	}
	if version.MimeType != "text/plain" {
		t.Fatalf("mime type %q, want text/plain", version.MimeType)
	}
	if version.CustomMetadata["origin"] != "test" {
		t.Fatalf("custom metadata %v, want origin=test", version.CustomMetadata)
	}
}

func TestNewRequiresABucket(t *testing.T) {
	if _, err := newService(newFakeS3(), "  "); err == nil {
		t.Fatal("expected an error without a bucket")
	}
}

// objectKeys is an object map's keys in order, for failure messages.
func objectKeys(objects map[string]fakeObject) []string {
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
