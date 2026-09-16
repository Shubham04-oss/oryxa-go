package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestAgentGrantLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	userID := "it-agent-user-" + suffix
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_agent_grants WHERE user_id = $1`, userID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM auth_users WHERE id = $1`, userID)
	}()

	now := time.Now().UTC()
	user := domain.User{ID: userID, Email: userID + "@example.com", CreatedAt: now, UpdatedAt: now}
	if _, err := store.UpsertFederatedUser(ctx, user, testEvent("it-agent-registered-"+suffix, "auth.user.registered.v1", now), testEvent("it-agent-signed-in-"+suffix, "auth.user.signed-in.v1", now)); err != nil {
		t.Fatalf("upsert user: %v", err)
	}

	if _, err := store.GetAgentGrant(ctx, userID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetAgentGrant(none) error = %v, want not found", err)
	}
	// A grant that has never existed still replaces cleanly to nothing and
	// deletes idempotently.
	if err := store.ReplaceAgentGrantToken(ctx, userID, domain.SealedData{Version: 1, Nonce: []byte("n"), Ciphertext: []byte("c"), EncryptedDataKey: []byte("k")}, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ReplaceAgentGrantToken(none) error = %v, want not found", err)
	}
	if err := store.DeleteAgentGrant(ctx, userID); err != nil {
		t.Fatalf("DeleteAgentGrant(none) error = %v, want nil", err)
	}

	sealed := domain.SealedData{Version: 1, Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext"), EncryptedDataKey: []byte("key")}
	grant := domain.AgentGrant{UserID: userID, EncryptedRefreshToken: sealed, Scope: "openid oryxa/agent.actions", CreatedAt: now}
	if err := store.UpsertAgentGrant(ctx, grant); err != nil {
		t.Fatalf("UpsertAgentGrant() error = %v", err)
	}
	got, err := store.GetAgentGrant(ctx, userID)
	if err != nil {
		t.Fatalf("GetAgentGrant() error = %v", err)
	}
	if got.UserID != userID || got.Scope != grant.Scope || got.RefreshedAt != nil ||
		string(got.EncryptedRefreshToken.Ciphertext) != "ciphertext" || string(got.EncryptedRefreshToken.Nonce) != "nonce" || string(got.EncryptedRefreshToken.EncryptedDataKey) != "key" {
		t.Fatalf("GetAgentGrant() = %+v", got)
	}

	// Re-authorizing upserts the whole row.
	reauthorized := now.Add(time.Minute)
	newSealed := domain.SealedData{Version: 1, Nonce: []byte("nonce-2"), Ciphertext: []byte("ciphertext-2"), EncryptedDataKey: []byte("key-2")}
	if err := store.UpsertAgentGrant(ctx, domain.AgentGrant{UserID: userID, EncryptedRefreshToken: newSealed, Scope: grant.Scope, CreatedAt: reauthorized}); err != nil {
		t.Fatalf("UpsertAgentGrant(again) error = %v", err)
	}
	got, err = store.GetAgentGrant(ctx, userID)
	if err != nil || !got.CreatedAt.Equal(reauthorized) || string(got.EncryptedRefreshToken.Ciphertext) != "ciphertext-2" {
		t.Fatalf("GetAgentGrant(after re-authorize) = %+v, %v", got, err)
	}

	// A rotation replaces only the sealed token and refreshed_at.
	rotated := reauthorized.Add(time.Hour)
	rotatedSealed := domain.SealedData{Version: 1, Nonce: []byte("nonce-3"), Ciphertext: []byte("ciphertext-3"), EncryptedDataKey: []byte("key-3")}
	if err := store.ReplaceAgentGrantToken(ctx, userID, rotatedSealed, rotated); err != nil {
		t.Fatalf("ReplaceAgentGrantToken() error = %v", err)
	}
	got, err = store.GetAgentGrant(ctx, userID)
	if err != nil || string(got.EncryptedRefreshToken.Ciphertext) != "ciphertext-3" || got.RefreshedAt == nil || !got.RefreshedAt.Equal(rotated) || !got.CreatedAt.Equal(reauthorized) {
		t.Fatalf("GetAgentGrant(after rotation) = %+v, %v", got, err)
	}

	if err := store.DeleteAgentGrant(ctx, userID); err != nil {
		t.Fatalf("DeleteAgentGrant() error = %v", err)
	}
	if _, err := store.GetAgentGrant(ctx, userID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetAgentGrant(deleted) error = %v, want not found", err)
	}
	// Deleting again is still not an error.
	if err := store.DeleteAgentGrant(ctx, userID); err != nil {
		t.Fatalf("DeleteAgentGrant(again) error = %v, want nil", err)
	}
}

func TestAgentGrantRequiresExistingUser(t *testing.T) {
	databaseURL := os.Getenv("AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	sealed := domain.SealedData{Version: 1, Nonce: []byte("n"), Ciphertext: []byte("c"), EncryptedDataKey: []byte("k")}
	err = store.UpsertAgentGrant(ctx, domain.AgentGrant{
		UserID: "it-agent-missing-user-" + suffix, EncryptedRefreshToken: sealed, Scope: "openid", CreatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpsertAgentGrant(no such user) error = %v, want conflict", err)
	}
}
