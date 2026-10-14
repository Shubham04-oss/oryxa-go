package serviceauth

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

func (stuckKMS) Sign(ctx context.Context, _ *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stuckKMS) GetPublicKey(ctx context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// withinTimeout runs call, which passes no deadline of its own, and fails the
// test unless it gives up with a deadline error once kmscall.Timeout passes.
func withinTimeout(t *testing.T, call func() error) {
	t.Helper()
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
}

func TestKMSCallsThatNeverAnswerGiveUp(t *testing.T) {
	t.Parallel()
	t.Run("signing a token", func(t *testing.T) {
		t.Parallel()
		signer := Signer{Client: stuckKMS{}, KeyID: "k1", Service: "integrations"}
		withinTimeout(t, func() error {
			_, err := signer.Token(context.Background(), "connections", "action")
			return err
		})
	})
	t.Run("fetching a caller's public key", func(t *testing.T) {
		t.Parallel()
		key := generateKey(t)
		token, err := Signer{Client: &fakeSignAPI{key: key}, KeyID: "k1", Service: "integrations"}.
			Token(context.Background(), "connections", "action")
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := NewVerifier(VerifierConfig{
			Keys:     stuckKMS{},
			Audience: "connections",
			Callers:  []Caller{{Service: "integrations", KeyID: "k1", Purposes: []string{"action"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		withinTimeout(t, func() error {
			_, err := verifier.Verify(context.Background(), token)
			return err
		})
		// The key lookup holds a lock every verification waits on, so a
		// caller's later deadline does not lift the bound.
		withinTimeout(t, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := verifier.Verify(ctx, token)
			return err
		})
	})
}
