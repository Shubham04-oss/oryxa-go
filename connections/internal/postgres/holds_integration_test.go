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

// TestAddHoldAndCredentialDeletionAreSerialized races a hold against the
// cleanup that deletes the credentials it means to keep. Before AddHold took
// its row lock, its unlocked EXISTS read did not conflict with the deletion's
// write, so the insert could land after the deletion had already removed the
// holds: the hold survived on a connection left with no credentials, callers
// believed the credentials were retained, and nothing ever removed the row.
// The invariant is that the two can never both win.
func TestAddHoldAndCredentialDeletionAreSerialized(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Enough attempts that the pre-fix interleaving shows up; it was observed
	// on 39 of 40 before the lock was added.
	const attempts = 40
	for attempt := range attempts {
		org := "org_hold_race_" + randomHex(4)
		suffix := randomHex(4)
		app := createApp(t, store, org, "app_hold_race_"+suffix, domain.CredentialOAuth2, now)
		connection := createConnection(t, store, app, "con_hold_race_"+suffix, "user-1", "account-1", now)
		disconnect(t, store, org, connection.ID, domain.DisconnectRequested, now)
		// Take the cleanup lease the way the worker would, so the deletion is
		// not refused for want of one.
		if _, err := store.pool.Exec(ctx, `
			UPDATE connections SET refresh_lease_token = $3, refresh_lease_until = $4
			WHERE organization_id = $1 AND id = $2`,
			org, connection.ID, "cleanup-lease", now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		holdDone := make(chan error, 1)
		deleteDone := make(chan error, 1)
		go func() {
			<-start
			holdDone <- store.AddHold(ctx, org, connection.ID, "integrations.webhooks", now)
		}()
		go func() {
			<-start
			deleteDone <- store.DeleteCredentials(ctx, org, connection.ID, "cleanup-lease", now,
				testEvent("connections.connection.credentials-deleted.v1", now))
		}()
		close(start)
		holdErr, deleteErr := <-holdDone, <-deleteDone

		// Whichever ran first, the loser must have seen the winner: either the
		// hold was refused or removed, or the deletion lost the race.
		var holds int
		if err := store.pool.QueryRow(ctx,
			`SELECT count(*) FROM connections_holds WHERE organization_id = $1 AND connection_id = $2`,
			org, connection.ID).Scan(&holds); err != nil {
			t.Fatal(err)
		}
		var credentialsDeleted bool
		if err := store.pool.QueryRow(ctx,
			`SELECT credentials_deleted_at IS NOT NULL FROM connections WHERE organization_id = $1 AND id = $2`,
			org, connection.ID).Scan(&credentialsDeleted); err != nil {
			t.Fatal(err)
		}
		if holds > 0 && credentialsDeleted {
			t.Fatalf("attempt %d: %d hold(s) survived on a connection whose credentials are deleted (hold err %v, delete err %v)",
				attempt, holds, holdErr, deleteErr)
		}
	}
}
