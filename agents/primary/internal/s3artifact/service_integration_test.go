package s3artifact

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
)

// TestS3RoundTrip runs the service against a real S3 API — the LocalStack
// emulator in docker-compose.yml, or AWS itself. It is skipped without an
// endpoint and a bucket:
//
//	AGENTS_PRIMARY_TEST_AWS_ENDPOINT=http://localhost:4566 \
//	AGENTS_PRIMARY_TEST_ARTIFACT_BUCKET=oryxa-agent-artifacts \
//	go test ./agents/primary/internal/s3artifact/ -run TestS3RoundTrip -v
//
// It writes under a session of its own and deletes what it wrote.
func TestS3RoundTrip(t *testing.T) {
	endpoint := os.Getenv("AGENTS_PRIMARY_TEST_AWS_ENDPOINT")
	bucket := os.Getenv("AGENTS_PRIMARY_TEST_ARTIFACT_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("AGENTS_PRIMARY_TEST_AWS_ENDPOINT and AGENTS_PRIMARY_TEST_ARTIFACT_BUCKET are not both set")
	}
	ctx := context.Background()

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "ap-south-1"
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		// The emulator takes any signature, and this test must not depend on
		// whatever credentials happen to be on the machine.
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load AWS configuration: %v", err)
	}
	service, err := NewFromConfig(awsConfig, endpoint, bucket)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	request := func(fileName string) *artifact.SaveRequest {
		return &artifact.SaveRequest{
			AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: fileName,
			Part: genai.NewPartFromBytes([]byte("name,score\nAlice,95\n"), "text/csv"),
		}
	}
	t.Cleanup(func() {
		for _, fileName := range []string{"scores.csv", "user:profile.json"} {
			if err := service.Delete(context.Background(), &artifact.DeleteRequest{
				AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: fileName,
			}); err != nil {
				t.Logf("clean up %s: %v", fileName, err)
			}
		}
	})

	first, err := service.Save(ctx, request("scores.csv"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version is %d, want 1", first.Version)
	}
	second, err := service.Save(ctx, request("scores.csv"))
	if err != nil {
		t.Fatalf("save again: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version is %d, want 2: the precondition did not hold", second.Version)
	}

	loaded, err := service.Load(ctx, &artifact.LoadRequest{
		AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: "scores.csv",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := string(loaded.Part.InlineData.Data); got != "name,score\nAlice,95\n" {
		t.Fatalf("loaded %q", got)
	}
	if got := loaded.Part.InlineData.MIMEType; got != "text/csv" {
		t.Fatalf("mime type %q, want text/csv", got)
	}

	// A user-namespaced artifact is reachable from another session.
	if _, err := service.Save(ctx, request("user:profile.json")); err != nil {
		t.Fatalf("save user artifact: %v", err)
	}
	elsewhere, err := service.List(ctx, &artifact.ListRequest{
		AppName: "krude", UserID: "user-owner", SessionID: "session-somewhere-else",
	})
	if err != nil {
		t.Fatalf("list from another session: %v", err)
	}
	if !slices.Contains(elsewhere.FileNames, "user:profile.json") {
		t.Fatalf("another session lists %v, want it to include user:profile.json", elsewhere.FileNames)
	}
	if slices.Contains(elsewhere.FileNames, "scores.csv") {
		t.Fatalf("another session lists %v, want it to exclude this session's scores.csv", elsewhere.FileNames)
	}

	described, err := service.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
		AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: "scores.csv",
	})
	if err != nil {
		t.Fatalf("get artifact version: %v", err)
	}
	want := fmt.Sprintf("s3://%s/krude/user-owner/%s/scores.csv/2", bucket, sessionID)
	if described.ArtifactVersion.CanonicalURI != want {
		t.Fatalf("canonical URI %q, want %q", described.ArtifactVersion.CanonicalURI, want)
	}

	if err := service.Delete(ctx, &artifact.DeleteRequest{
		AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: "scores.csv",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := service.Load(ctx, &artifact.LoadRequest{
		AppName: "krude", UserID: "user-owner", SessionID: sessionID, FileName: "scores.csv",
	}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load after delete = %v, want fs.ErrNotExist", err)
	}
}
