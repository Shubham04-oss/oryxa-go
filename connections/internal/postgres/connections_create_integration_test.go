package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestCreateConnectionAndReplaceCredentials(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_create_connection"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	first := domain.Connection{
		ID: "con_a", OrganizationID: org, AppID: app.ID, Provider: app.Provider, CredentialType: app.CredentialType,
		Scope: domain.ScopeMember, MemberID: "user-1", ProviderAccount: domain.ProviderAccount{ID: "account-1", Name: "a@example.com"},
		Scopes: app.Scopes, Status: domain.StatusActive, StatusChangedAt: now, EncryptedCredentials: sealed("a"),
		CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.CreateConnection(ctx, first, testEvent("connections.connection.connected.v1", now), now.Add(24*time.Hour))
	if err != nil || !created.Default || created.Version != 1 || created.Usage != domain.UsageMembers || created.Health.State != domain.HealthUnknown {
		t.Fatalf("CreateConnection() = %+v, %v", created, err)
	}

	second := first
	second.ID, second.ProviderAccount.ID = "con_b", "account-2"
	secondCreated, err := store.CreateConnection(ctx, second, testEvent("connections.connection.connected.v1", now), now.Add(24*time.Hour))
	if err != nil || secondCreated.Default {
		t.Fatalf("second CreateConnection() = %+v, %v", secondCreated, err)
	}

	dup := first
	dup.ID = "con_dup"
	if _, err := store.CreateConnection(ctx, dup, testEvent("connections.connection.connected.v1", now), now); !errors.Is(err, domain.ErrAlreadyConnected) {
		t.Fatalf("duplicate CreateConnection() error = %v", err)
	}

	replacement := created
	replacement.EncryptedCredentials = sealed("a-renewed")
	replacement.Scopes = []string{"email", "profile"}
	replacement.ProviderAccount.Name = "renamed@example.com"
	replacement.UpdatedAt = now.Add(time.Minute)
	replacement.StatusChangedAt = now.Add(time.Minute)
	replaced, err := store.ReplaceCredentials(ctx, replacement, created.Version, testEvent("connections.connection.reconnected.v1", now))
	if err != nil || replaced.Version != created.Version+1 {
		t.Fatalf("ReplaceCredentials() = %+v, %v", replaced, err)
	}
	reloaded, err := store.Connection(ctx, org, created.ID)
	if err != nil || reloaded.ProviderAccount.Name != "renamed@example.com" || !slices.Equal(reloaded.Scopes, []string{"email", "profile"}) ||
		reloaded.EncryptedCredentials == nil || string(reloaded.EncryptedCredentials.Ciphertext) != "ciphertext-a-renewed" {
		t.Fatalf("connection after ReplaceCredentials() = %+v, %v", reloaded, err)
	}

	// The version guard rejects a stale caller.
	if _, err := store.ReplaceCredentials(ctx, replacement, created.Version, testEvent("connections.connection.reconnected.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale ReplaceCredentials() error = %v", err)
	}

	// A disconnected connection can no longer have its credentials replaced.
	disconnect(t, store, org, second.ID, domain.DisconnectRequested, now.Add(2*time.Minute))
	stale := secondCreated
	stale.EncryptedCredentials = sealed("b-renewed")
	stale.UpdatedAt, stale.StatusChangedAt = now.Add(3*time.Minute), now.Add(3*time.Minute)
	if _, err := store.ReplaceCredentials(ctx, stale, secondCreated.Version, testEvent("connections.connection.reconnected.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ReplaceCredentials() on a disconnected connection error = %v", err)
	}

	claimed, err := store.ClaimOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, event := range claimed {
		kinds[event.DetailType]++
	}
	if kinds["connections.connection.connected.v1"] != 2 || kinds["connections.connection.reconnected.v1"] != 1 || kinds["connections.connection.disconnected.v1"] != 1 {
		t.Fatalf("outbox events = %+v", kinds)
	}
}

func TestSharesProviderGrant(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_shared_grant"
	appA := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	appB := createApp(t, store, org, "app_slack", domain.CredentialOAuth2, now)

	memberConn := createConnection(t, store, appA, "con_member", "user-1", "account-1", now)
	teamConn := createScopedConnection(t, store, appA, "con_team", domain.ScopeTeam, "team-1", "", "account-1", now)

	if shared, err := store.SharesProviderGrant(ctx, org, appA.ID, "account-1", memberConn.ID); err != nil || !shared {
		t.Fatalf("SharesProviderGrant(another live connection) = %v, %v, want true", shared, err)
	}
	if shared, err := store.SharesProviderGrant(ctx, org, appA.ID, "account-1", ""); err != nil || !shared {
		t.Fatalf("SharesProviderGrant(no exception) = %v, %v, want true", shared, err)
	}
	if shared, err := store.SharesProviderGrant(ctx, org, appB.ID, "account-1", ""); err != nil || shared {
		t.Fatalf("SharesProviderGrant(different app) = %v, %v, want false", shared, err)
	}
	if shared, err := store.SharesProviderGrant(ctx, org, appA.ID, "account-does-not-exist", ""); err != nil || shared {
		t.Fatalf("SharesProviderGrant(unknown account) = %v, %v, want false", shared, err)
	}

	// Disconnect and delete the team connection's credentials: it no longer
	// shares the grant, even though its row is still there.
	disconnect(t, store, org, teamConn.ID, domain.DisconnectRequested, now.Add(time.Minute))
	claimed, err := store.ClaimCredentialDeletions(ctx, now.Add(2*time.Minute), 10, "claim-shared-grant", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != teamConn.ID {
		t.Fatalf("ClaimCredentialDeletions() = %+v, %v", claimed, err)
	}
	if err := store.DeleteCredentials(ctx, org, teamConn.ID, "claim-shared-grant", now.Add(4*time.Minute),
		testEvent("connections.connection.credentials-deleted.v1", now)); err != nil {
		t.Fatal(err)
	}

	if shared, err := store.SharesProviderGrant(ctx, org, appA.ID, "account-1", memberConn.ID); err != nil || shared {
		t.Fatalf("SharesProviderGrant(only credentials-deleted others) = %v, %v, want false", shared, err)
	}
}
