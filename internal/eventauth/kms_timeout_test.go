package eventauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/kmscall"
)

// stuckKMS accepts every call and never answers, like a KMS endpoint that
// holds the connection open: it returns only when the call's context ends.
type stuckKMS struct{}

func (stuckKMS) Sign(ctx context.Context, _ *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stuckKMS) GetPublicKey(ctx context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Signing and verifying take no context, so before kmscall nothing could
// bound these calls at all.
func TestKMSCallsThatNeverAnswerGiveUp(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for name, call := range map[string]func(t *testing.T) error{
		"signing an event": func(t *testing.T) error {
			signer, err := NewKMSSigner(stuckKMS{}, "key-teams", func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			_, err = signer.Sign("oryxa.teams", "teams.team.created.v1", "event-1", 1, "2026-10-10T00:00:00Z", []byte(`{}`))
			return err
		},
		"fetching a service's public key": func(t *testing.T) error {
			// A well-formed signature, so the check reaches the key lookup.
			local, _, err := NewLocalSigner("teams", func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			sig, err := local.Sign("oryxa.teams", "teams.team.created.v1", "event-1", 1, "2026-10-10T00:00:00Z", []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			verifier := NewVerifier(nil, &KMSKeys{Client: stuckKMS{}, KeyIDs: map[string]string{"teams": "key-teams"}},
				func() time.Time { return now })
			err = verifier.Verify("oryxa.teams", "teams.team.created.v1", "event-1", 1, "2026-10-10T00:00:00Z", []byte(`{}`), sig)
			if err != nil && !errors.Is(err, ErrUnverified) {
				t.Errorf("error %v is not ErrUnverified", err)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { done <- call(t) }()
			select {
			case err := <-done:
				// The verifier reports the cause in its message, not its chain.
				if err == nil || !(errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
					t.Fatalf("error is %v, want a context deadline", err)
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
