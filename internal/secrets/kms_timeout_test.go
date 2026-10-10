package secrets

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/kmscall"
)

// stuckKMS accepts every call and never answers, like a KMS endpoint that
// holds the connection open: it returns only when the call's context ends.
type stuckKMS struct{}

func (stuckKMS) GenerateDataKey(ctx context.Context, _ *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stuckKMS) Decrypt(ctx context.Context, _ *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestKMSCallsThatNeverAnswerGiveUp(t *testing.T) {
	t.Parallel()
	keys := KMSDataKeys{Client: stuckKMS{}, KeyID: "key-id"}
	binding := Binding{ResourceType: "credential", ResourceID: "connection-1"}.Context("oryxa-apps")
	for name, call := range map[string]func() error{
		"generating a data key": func() error {
			_, _, err := keys.GenerateDataKey(context.Background(), "", binding)
			return err
		},
		"decrypting a data key": func() error {
			_, err := keys.DecryptDataKey(context.Background(), []byte("wrapped"), binding)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- call() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error is %v, want context.DeadlineExceeded", err)
				}
				if elapsed := time.Since(start); elapsed < kmscall.Timeout {
					t.Fatalf("gave up after %v, before the %v timeout", elapsed, kmscall.Timeout)
				}
			case <-time.After(kmscall.Timeout + 5*time.Second):
				t.Fatalf("still waiting on KMS after %v", kmscall.Timeout+5*time.Second)
			}
		})
	}
}
