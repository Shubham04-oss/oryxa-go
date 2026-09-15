package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestHolds(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_holds"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	if count, err := store.CountHolds(ctx, org, connection.ID); err != nil || count != 0 {
		t.Fatalf("CountHolds() initial = %d, %v, want 0", count, err)
	}
	if err := store.AddHold(ctx, org, connection.ID, "integrations.webhooks", now); err != nil {
		t.Fatal(err)
	}
	// Adding the same holder again is fine.
	if err := store.AddHold(ctx, org, connection.ID, "integrations.webhooks", now.Add(time.Minute)); err != nil {
		t.Fatalf("AddHold() again error = %v, want nil", err)
	}
	if err := store.AddHold(ctx, org, connection.ID, "integrations.triggers", now); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountHolds(ctx, org, connection.ID); err != nil || count != 2 {
		t.Fatalf("CountHolds() = %d, %v, want 2", count, err)
	}

	if err := store.AddHold(ctx, org, "con_missing", "holder", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AddHold(missing connection) error = %v, want ErrNotFound", err)
	}

	if err := store.ReleaseHold(ctx, org, connection.ID, "integrations.webhooks"); err != nil {
		t.Fatal(err)
	}
	// Releasing an absent hold is fine.
	if err := store.ReleaseHold(ctx, org, connection.ID, "integrations.webhooks"); err != nil {
		t.Fatalf("ReleaseHold() again error = %v, want nil", err)
	}
	if count, err := store.CountHolds(ctx, org, connection.ID); err != nil || count != 1 {
		t.Fatalf("CountHolds() after release = %d, %v, want 1", count, err)
	}

	// A hold survives disconnect: the connection is disconnected but its
	// credentials are not deleted yet.
	disconnect(t, store, org, connection.ID, domain.DisconnectRequested, now.Add(2*time.Minute))
	if err := store.AddHold(ctx, org, connection.ID, "integrations.cleanup", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("AddHold() on a disconnected connection error = %v, want nil", err)
	}

	// Release every hold and let cleanup delete the credentials.
	if err := store.ReleaseHold(ctx, org, connection.ID, "integrations.triggers"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseHold(ctx, org, connection.ID, "integrations.cleanup"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountHolds(ctx, org, connection.ID); err != nil || count != 0 {
		t.Fatalf("CountHolds() before cleanup = %d, %v, want 0", count, err)
	}
	claimed, err := store.ClaimCredentialDeletions(ctx, now.Add(4*time.Minute), 10, "claim-holds", now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != connection.ID {
		t.Fatalf("ClaimCredentialDeletions() = %+v, want just %s", claimed, connection.ID)
	}
	if err := store.DeleteCredentials(ctx, org, connection.ID, "claim-holds", now.Add(6*time.Minute),
		testEvent("connections.connection.credentials-deleted.v1", now)); err != nil {
		t.Fatal(err)
	}

	// A connection whose credentials are already deleted refuses new holds.
	if err := store.AddHold(ctx, org, connection.ID, "holder-after-delete", now.Add(7*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AddHold() after credentials deleted error = %v, want ErrNotFound", err)
	}
	if count, err := store.CountHolds(ctx, org, connection.ID); err != nil || count != 0 {
		t.Fatalf("CountHolds() after DeleteCredentials = %d, %v, want 0 (holds were removed)", count, err)
	}
}
