package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// TestClaimCredentialDeletionsWaitsForHoldsButNotPastMaxHold checks the two
// conditions that let a disconnected connection's credentials be claimed for
// deletion: no open hold, or MaxHold has passed since it disconnected even
// with a hold still open.
func TestClaimCredentialDeletionsWaitsForHoldsButNotPastMaxHold(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_claim_deletions"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	held := createConnection(t, store, app, "con_held", "user-1", "account-1", now)
	free := createConnection(t, store, app, "con_free", "user-1", "account-2", now)
	live := createConnection(t, store, app, "con_live", "user-1", "account-3", now)

	disconnectedAt := now.Add(time.Minute)
	disconnect(t, store, org, held.ID, domain.DisconnectRequested, disconnectedAt)
	disconnect(t, store, org, free.ID, domain.DisconnectRequested, disconnectedAt)

	if err := store.AddHold(ctx, org, held.ID, "integrations.webhooks", disconnectedAt); err != nil {
		t.Fatal(err)
	}

	// Right after disconnect: the held connection is skipped; the free one
	// qualifies; the still-live one is never a candidate.
	claimed, err := store.ClaimCredentialDeletions(ctx, disconnectedAt.Add(time.Second), 10, "claim-1", disconnectedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != free.ID {
		t.Fatalf("ClaimCredentialDeletions() right after disconnect = %+v, want just %s", claimed, free.ID)
	}
	if err := store.DeleteCredentials(ctx, org, free.ID, "claim-1", disconnectedAt.Add(2*time.Second),
		testEvent("connections.connection.credentials-deleted.v1", now)); err != nil {
		t.Fatal(err)
	}

	// Just before MaxHold has passed: the open hold still blocks it.
	almostMaxHold := disconnectedAt.Add(domain.MaxHold - time.Minute)
	claimed, err = store.ClaimCredentialDeletions(ctx, almostMaxHold, 10, "claim-2", almostMaxHold.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("ClaimCredentialDeletions() just before MaxHold = %+v, want empty", claimed)
	}

	// Once MaxHold has passed, the hold no longer blocks it.
	pastMaxHold := disconnectedAt.Add(domain.MaxHold + time.Minute)
	claimed, err = store.ClaimCredentialDeletions(ctx, pastMaxHold, 10, "claim-3", pastMaxHold.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != held.ID || claimed[0].EncryptedCredentials == nil {
		t.Fatalf("ClaimCredentialDeletions() past MaxHold = %+v, want just %s with credentials still present", claimed, held.ID)
	}

	// A live connection is never a candidate, regardless of time.
	for _, connection := range claimed {
		if connection.ID == live.ID {
			t.Fatalf("live connection was claimed: %+v", claimed)
		}
	}

	if count, err := store.CountHolds(ctx, org, held.ID); err != nil || count != 1 {
		t.Fatalf("CountHolds() before DeleteCredentials = %d, %v, want 1", count, err)
	}
	if err := store.DeleteCredentials(ctx, org, held.ID, "claim-3", pastMaxHold.Add(2*time.Minute),
		testEvent("connections.connection.credentials-deleted.v1", now)); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountHolds(ctx, org, held.ID); err != nil || count != 0 {
		t.Fatalf("CountHolds() after DeleteCredentials = %d, %v, want 0", count, err)
	}
	var healthRows int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM connections_health WHERE organization_id = $1 AND connection_id = $2`,
		org, held.ID).Scan(&healthRows); err != nil {
		t.Fatal(err)
	}
	if healthRows != 0 {
		t.Fatalf("connections_health rows after DeleteCredentials = %d, want 0", healthRows)
	}

	// The row left behind satisfies connections_credentials_check: no
	// credentials, a deletion time, and still disconnected.
	reloaded, err := store.Connection(ctx, org, held.ID)
	if err != nil || reloaded.EncryptedCredentials != nil || reloaded.CredentialsDeletedAt == nil || reloaded.Status != domain.StatusDisconnected {
		t.Fatalf("connection after DeleteCredentials = %+v, %v", reloaded, err)
	}

	// A replayed or stale token fails.
	if err := store.DeleteCredentials(ctx, org, held.ID, "claim-3", pastMaxHold.Add(3*time.Minute),
		testEvent("connections.connection.credentials-deleted.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("DeleteCredentials() replayed error = %v, want ErrConflict", err)
	}

	if reloaded, err := store.Connection(ctx, org, live.ID); err != nil || reloaded.Status != domain.StatusActive {
		t.Fatalf("live connection = %+v, %v, want unchanged", reloaded, err)
	}
}

// TestDeleteCredentialsRequiresTheClaimedLease confirms DeleteCredentials
// only works under the lease ClaimCredentialDeletions handed out, and that a
// live or already-cleaned-up connection refuses it.
func TestDeleteCredentialsRequiresTheClaimedLease(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_delete_credentials"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	// A live connection: no lease to claim it under, so any token fails.
	if err := store.DeleteCredentials(ctx, org, connection.ID, "no-such-lease", now, testEvent("connections.connection.credentials-deleted.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("DeleteCredentials(live connection) error = %v, want ErrConflict", err)
	}

	disconnect(t, store, org, connection.ID, domain.DisconnectRequested, now.Add(time.Minute))
	claimed, err := store.ClaimCredentialDeletions(ctx, now.Add(2*time.Minute), 10, "claim-1", now.Add(3*time.Minute))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimCredentialDeletions() = %+v, %v", claimed, err)
	}

	// The wrong token fails even though a lease is held.
	if err := store.DeleteCredentials(ctx, org, connection.ID, "wrong-token", now.Add(4*time.Minute), testEvent("connections.connection.credentials-deleted.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("DeleteCredentials(wrong token) error = %v, want ErrConflict", err)
	}

	event := testEvent("connections.connection.credentials-deleted.v1", now)
	if err := store.DeleteCredentials(ctx, org, connection.ID, "claim-1", now.Add(5*time.Minute), event); err != nil {
		t.Fatal(err)
	}

	var found bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connections_outbox WHERE id = $1)`, event.ID).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("DeleteCredentials() did not record its event in the outbox")
	}
}
