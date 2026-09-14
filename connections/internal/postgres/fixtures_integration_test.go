package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// createApp inserts a minimal active app directly through the store, for
// tests that need an app to hang connections off but are not exercising app
// behavior themselves.
func createApp(t *testing.T, store *Store, organizationID, appID string, credentialType domain.CredentialType, at time.Time) domain.App {
	t.Helper()
	app := domain.App{
		ID: appID, OrganizationID: organizationID, Provider: "google", CredentialType: credentialType,
		ClientID: "client-1", EncryptedSecret: sealed("secret-" + appID), Scopes: []string{"email", "profile"},
		Status: domain.AppStatusActive, Revision: 1, CreatedBy: "user-admin", CreatedAt: at, UpdatedAt: at,
	}
	if err := store.CreateApp(context.Background(), app, testEvent("connections.app.created.v1", at)); err != nil {
		t.Fatalf("CreateApp(%s) error = %v", appID, err)
	}
	return app
}

// createScopedConnection makes a live connection through CreateConnection,
// the session-less twin of CompleteConnect, at the given placement.
func createScopedConnection(t *testing.T, store *Store, app domain.App, id string, scope domain.Scope, teamID, memberID, account string, at time.Time) domain.Connection {
	t.Helper()
	createdBy := memberID
	if createdBy == "" {
		createdBy = "user-admin"
	}
	connection := domain.Connection{
		ID: id, OrganizationID: app.OrganizationID, AppID: app.ID, Provider: app.Provider, CredentialType: app.CredentialType,
		Scope: scope, TeamID: teamID, MemberID: memberID,
		ProviderAccount: domain.ProviderAccount{ID: account, Name: account + "@example.com"},
		Scopes:          app.Scopes, Status: domain.StatusActive, StatusChangedAt: at, EncryptedCredentials: sealed(id),
		CreatedBy: createdBy, CreatedAt: at, UpdatedAt: at,
	}
	created, err := store.CreateConnection(context.Background(), connection, testEvent("connections.connection.connected.v1", at), at.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateConnection(%s) error = %v", id, err)
	}
	return created
}

// createConnection makes a live member-scoped connection.
func createConnection(t *testing.T, store *Store, app domain.App, id, memberID, account string, at time.Time) domain.Connection {
	t.Helper()
	return createScopedConnection(t, store, app, id, domain.ScopeMember, "", memberID, account, at)
}

// disconnect ends a connection for the given reason, discarding its event.
func disconnect(t *testing.T, store *Store, organizationID, connectionID string, reason domain.DisconnectReason, at time.Time) {
	t.Helper()
	if _, err := store.DisconnectConnection(context.Background(), organizationID, connectionID, reason, at,
		func(domain.Connection) (domain.Event, error) {
			return testEvent("connections.connection.disconnected.v1", at), nil
		}); err != nil {
		t.Fatalf("DisconnectConnection(%s) error = %v", connectionID, err)
	}
}
