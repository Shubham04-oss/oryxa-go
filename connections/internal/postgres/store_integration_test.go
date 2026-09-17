package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// openTestStore migrates a fresh schema and drops it afterwards, so the test
// never leaves tables in a shared development database.
// CONNECTIONS_TEST_DATABASE_URL must be a postgres:// URL.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("CONNECTIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONNECTIONS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schemaName := "connections_test_" + randomHex(6)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatalf("CONNECTIONS_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()

	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return store
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func violates(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

const (
	uniqueViolation = "23505"
	checkViolation  = "23514"
)

func TestSchemaEnforcesPlacementStatusAndUniqueness(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	org := "org_schema"

	oauthApp, clientApp := org+"_oauth", org+"_client"
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_apps (id, organization_id, provider, credential_type, created_by, created_at, updated_at)
		VALUES ($1, $3, 'google', 'oauth2', 'user-1', $4, $4),
		       ($2, $3, 'microsoft', 'oauth2_client_credentials', 'user-1', $4, $4)`,
		oauthApp, clientApp, org, now); err != nil {
		t.Fatal(err)
	}

	member, team := "user-1", "team_1"
	insert := func(id, appID, credentialType, scope string, teamID, memberID *string, account string, isDefault bool, status, reason string) error {
		_, err := store.pool.Exec(ctx, `
			INSERT INTO connections (
				id, organization_id, app_id, provider, credential_type, scope, team_id, member_id,
				provider_account_id, is_default, status, status_reason, status_changed_at,
				encrypted_credentials, created_by, created_at, updated_at
			) VALUES ($1, $2, $3, 'google', $4, $5, $6, $7, $8, $9, $10, $11, $12, '{}'::jsonb, 'user-1', $12, $12)`,
			org+"_"+id, org, appID, credentialType, scope, teamID, memberID, account, isDefault, status, reason, now)
		return err
	}

	if err := insert("first", oauthApp, "oauth2", "member", nil, &member, "account-1", true, "active", ""); err != nil {
		t.Fatalf("first connection: %v", err)
	}
	if err := insert("duplicate", oauthApp, "oauth2", "member", nil, &member, "account-1", false, "active", ""); !violates(err, uniqueViolation) {
		t.Fatalf("second live connection to the same account error = %v", err)
	}
	if err := insert("second-default", oauthApp, "oauth2", "member", nil, &member, "account-2", true, "active", ""); !violates(err, uniqueViolation) {
		t.Fatalf("second default error = %v", err)
	}
	if err := insert("second-account", oauthApp, "oauth2", "member", nil, &member, "account-2", false, "active", ""); err != nil {
		t.Fatalf("second account for the same app: %v", err)
	}
	if err := insert("team-account", oauthApp, "oauth2", "team", &team, nil, "account-1", true, "active", ""); err != nil {
		t.Fatalf("same account in another scope: %v", err)
	}
	if err := insert("member-with-team", oauthApp, "oauth2", "member", &team, &member, "account-3", false, "active", ""); !violates(err, checkViolation) {
		t.Fatalf("member connection naming a team error = %v", err)
	}
	if err := insert("member-client", clientApp, "oauth2_client_credentials", "member", nil, &member, "client-1", false, "active", ""); !violates(err, checkViolation) {
		t.Fatalf("member client-credentials connection error = %v", err)
	}
	if err := insert("wrong-reason", oauthApp, "oauth2", "team", &team, nil, "account-4", false, "needs_reconnect", "member_left"); !violates(err, checkViolation) {
		t.Fatalf("mismatched status reason error = %v", err)
	}

	if _, err := store.pool.Exec(ctx, `
		UPDATE connections SET status = 'disconnected', status_reason = 'disconnected', is_default = false
		WHERE id = $1`, org+"_first"); err != nil {
		t.Fatal(err)
	}
	if err := insert("reconnected", oauthApp, "oauth2", "member", nil, &member, "account-1", true, "active", ""); err != nil {
		t.Fatalf("new connection after the old one disconnected: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections SET encrypted_credentials = NULL, credentials_deleted_at = $2
		WHERE id = $1`, org+"_second-account", now); !violates(err, checkViolation) {
		t.Fatalf("deleting a live connection's credentials error = %v", err)
	}

	sessionInsert := `
		INSERT INTO connections_sessions (
			state_hash, handle_hash, organization_id, member_id, app_id, intent, scope,
			return_uri, redirect_uri, created_at, expires_at
		) VALUES ($1, $2, $3, 'user-1', $4, $5, 'member', 'https://app.example.test',
			'https://connect.example.test/v1/oauth/callback/google', $6, $7)`
	if _, err := store.pool.Exec(ctx, sessionInsert, strings.Repeat("a", 64), nil, org, oauthApp, "reconnect", now, now.Add(10*time.Minute)); !violates(err, checkViolation) {
		t.Fatalf("reconnect session without a connection error = %v", err)
	}
	if _, err := store.pool.Exec(ctx, sessionInsert, strings.Repeat("b", 64), strings.Repeat("c", 64), org, oauthApp, "connect", now, now.Add(10*time.Minute)); !violates(err, checkViolation) {
		t.Fatalf("session with a handle before its callback error = %v", err)
	}
}

func sealed(label string) *domain.SealedData {
	return &domain.SealedData{Version: 1, Nonce: []byte("nonce-" + label), Ciphertext: []byte("ciphertext-" + label), EncryptedDataKey: []byte("key")}
}

func testEvent(detailType string, at time.Time) domain.Event {
	return domain.Event{ID: "evt_" + randomHex(8), DetailType: detailType, OccurredAt: at, Data: json.RawMessage(`{}`)}
}

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestAppAndConnectLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_flow"

	app := domain.App{ID: "app_google", OrganizationID: org, Provider: "google", CredentialType: domain.CredentialOAuth2,
		ClientID: "client-1", EncryptedSecret: sealed("secret"), Scopes: []string{"email", "openid", "profile"},
		Status: domain.AppStatusActive, Revision: 1, CreatedBy: "user-admin", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateApp(ctx, app, testEvent("connections.app.created.v1", now)); err != nil {
		t.Fatalf("CreateApp() error = %v", err)
	}
	got, err := store.App(ctx, org, app.ID)
	if err != nil || got.ClientID != "client-1" || got.EncryptedSecret == nil || string(got.EncryptedSecret.Ciphertext) != "ciphertext-secret" ||
		!slices.Equal(got.Scopes, app.Scopes) || got.Revision != 1 || got.Status != domain.AppStatusActive {
		t.Fatalf("App() = %+v %v", got, err)
	}
	if apps, err := store.Apps(ctx, org); err != nil || len(apps) != 1 {
		t.Fatalf("Apps() = %+v %v", apps, err)
	}
	if _, err := store.App(ctx, "org_other", app.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("App() from another organization error = %v", err)
	}

	startSession := func(label, memberID string, intent domain.SessionIntent, connectionID string) string {
		t.Helper()
		state := sha("state-" + label)
		session := domain.Session{StateHash: state, OrganizationID: org, MemberID: memberID, AppID: app.ID, Intent: intent,
			ConnectionID: connectionID, Scope: domain.ScopeMember, ReturnURI: "https://app.example.test/done",
			RedirectURI: "https://connect.example.test/v1/oauth/callback/google", RequestedScopes: app.Scopes,
			EncryptedVerifier: sealed("verifier"), CreatedAt: now, ExpiresAt: now.Add(domain.SessionTTL)}
		if err := store.CreateSession(ctx, session); err != nil {
			t.Fatalf("CreateSession(%s) error = %v", label, err)
		}
		if err := store.RecordCallback(ctx, state, sha("handle-"+label), sealed("code"), "", now); err != nil {
			t.Fatalf("RecordCallback(%s) error = %v", label, err)
		}
		if err := store.ClaimSession(ctx, state, now); err != nil {
			t.Fatalf("ClaimSession(%s) error = %v", label, err)
		}
		return state
	}
	connect := func(label, memberID, account string) (domain.Connection, error) {
		state := startSession(label, memberID, domain.IntentConnect, "")
		connection := domain.Connection{ID: "con_" + label, OrganizationID: org, AppID: app.ID, Provider: "google",
			CredentialType: domain.CredentialOAuth2, Scope: domain.ScopeMember, MemberID: memberID,
			ProviderAccount: domain.ProviderAccount{ID: account, Name: account + "@example.com"}, Scopes: app.Scopes,
			Status: domain.StatusActive, StatusChangedAt: now, EncryptedCredentials: sealed("grant-" + label),
			CreatedBy: memberID, CreatedAt: now, UpdatedAt: now}
		return store.CompleteConnect(ctx, state, connection, testEvent("connections.connection.connected.v1", now), now.Add(24*time.Hour))
	}

	first, err := connect("first", "user-1", "account-1")
	if err != nil || !first.Default || first.Usage != domain.UsageMembers || first.Health.State != domain.HealthUnknown || first.Version != 1 {
		t.Fatalf("first CompleteConnect() = %+v %v", first, err)
	}
	second, err := connect("second", "user-1", "account-2")
	if err != nil || second.Default {
		t.Fatalf("second CompleteConnect() = %+v %v", second, err)
	}
	if _, err := connect("duplicate", "user-1", "account-1"); !errors.Is(err, domain.ErrAlreadyConnected) {
		t.Fatalf("duplicate account error = %v", err)
	}
	other, err := connect("other-member", "user-2", "account-1")
	if err != nil || !other.Default {
		t.Fatalf("other member CompleteConnect() = %+v %v", other, err)
	}

	completed, err := store.SessionByHandle(ctx, sha("handle-first"))
	if err != nil || completed.CompletedAt == nil || completed.EncryptedResult == nil || completed.StateHash != sha("state-first") ||
		completed.EncryptedVerifier == nil || !slices.Equal(completed.RequestedScopes, app.Scopes) {
		t.Fatalf("SessionByHandle() = %+v %v", completed, err)
	}
	if err := store.RecordCallback(ctx, sha("state-first"), sha("handle-again"), sealed("code"), "", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second callback error = %v", err)
	}
	replay := first
	replay.ID = "con_replay"
	if _, err := store.CompleteConnect(ctx, sha("state-first"), replay, testEvent("connections.connection.connected.v1", now), now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("completing a used session error = %v", err)
	}

	deniedState := sha("state-denied")
	if err := store.CreateSession(ctx, domain.Session{StateHash: deniedState, OrganizationID: org, MemberID: "user-1", AppID: app.ID,
		Intent: domain.IntentConnect, Scope: domain.ScopeMember, ReturnURI: "https://app.example.test/done",
		RedirectURI: "https://connect.example.test/v1/oauth/callback/google", CreatedAt: now, ExpiresAt: now.Add(domain.SessionTTL)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCallback(ctx, deniedState, "", nil, "provider_denied", now); err != nil {
		t.Fatalf("RecordCallback(denied) error = %v", err)
	}
	if denied, err := store.SessionByState(ctx, deniedState); err != nil || denied.ProviderError != "provider_denied" || denied.HandleHash != "" || denied.CallbackAt == nil {
		t.Fatalf("SessionByState(denied) = %+v %v", denied, err)
	}
	if err := store.ClaimSession(ctx, deniedState, now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("ClaimSession(denied) error = %v", err)
	}
	if err := store.BurnSession(ctx, deniedState, now); err != nil {
		t.Fatalf("BurnSession() error = %v", err)
	}
	if err := store.ClaimSession(ctx, sha("state-first"), now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("claiming a completed session error = %v", err)
	}

	// A session is claimed once, before it expires, and only a claimed session
	// can be consumed.
	unclaimedState := sha("state-unclaimed")
	if err := store.CreateSession(ctx, domain.Session{StateHash: unclaimedState, OrganizationID: org, MemberID: "user-1", AppID: app.ID,
		Intent: domain.IntentConnect, Scope: domain.ScopeMember, ReturnURI: "https://app.example.test/done",
		RedirectURI: "https://connect.example.test/v1/oauth/callback/google", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCallback(ctx, unclaimedState, sha("handle-unclaimed"), sealed("code"), "", now); err != nil {
		t.Fatal(err)
	}
	unclaimed := first
	unclaimed.ID, unclaimed.ProviderAccount.ID = "con_unclaimed", "account-9"
	if _, err := store.CompleteConnect(ctx, unclaimedState, unclaimed, testEvent("connections.connection.connected.v1", now), now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("completing an unclaimed session error = %v", err)
	}
	if err := store.ClaimSession(ctx, unclaimedState, now.Add(2*time.Minute)); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("claiming an expired session error = %v", err)
	}
	if err := store.ClaimSession(ctx, unclaimedState, now); err != nil {
		t.Fatalf("ClaimSession() error = %v", err)
	}
	if err := store.ClaimSession(ctx, unclaimedState, now); !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("claiming a session twice error = %v", err)
	}
	if claimed, err := store.SessionByState(ctx, unclaimedState); err != nil || claimed.ClaimedAt == nil || claimed.CompletedAt != nil ||
		claimed.Usage != domain.UsageMembers {
		t.Fatalf("SessionByState(claimed) = %+v %v", claimed, err)
	}

	mine, err := store.Connections(ctx, org, domain.ConnectionQuery{MemberID: "user-1"})
	if err != nil || len(mine) != 2 || mine[0].ID != first.ID || mine[1].ID != second.ID {
		t.Fatalf("member Connections() = %+v %v", mine, err)
	}
	everyone, err := store.Connections(ctx, org, domain.ConnectionQuery{MemberID: "user-admin", Admin: true})
	if err != nil || len(everyone) != 3 {
		t.Fatalf("admin Connections() = %+v %v", everyone, err)
	}
	fetched, err := store.Connection(ctx, org, first.ID)
	if err != nil || fetched.ProviderAccount != first.ProviderAccount || fetched.EncryptedCredentials == nil || !fetched.Default ||
		fetched.Health.State != domain.HealthUnknown || fetched.Blocked != "" {
		t.Fatalf("Connection() = %+v %v", fetched, err)
	}

	updated := app
	updated.ClientID, updated.EncryptedSecret, updated.Revision, updated.UpdatedAt = "client-2", sealed("secret-2"), 2, now.Add(time.Minute)
	marked := 0
	err = store.UpdateApp(ctx, updated, 1, domain.ReconnectClientChanged, testEvent("connections.app.updated.v1", now),
		func(connection domain.Connection) (domain.Event, error) {
			marked++
			if connection.Status != domain.StatusNeedsReconnect || connection.Reason != "client_changed" {
				t.Errorf("connection marked as %s/%s", connection.Status, connection.Reason)
			}
			return testEvent("connections.connection.needs-reconnect.v1", now), nil
		})
	if err != nil || marked != 3 {
		t.Fatalf("UpdateApp() marked %d, error = %v", marked, err)
	}
	if err := store.UpdateApp(ctx, updated, 1, "", testEvent("connections.app.updated.v1", now), nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale UpdateApp() error = %v", err)
	}

	reconnectState := startSession("reconnect", "user-1", domain.IntentReconnect, first.ID)
	current, err := store.Connection(ctx, org, first.ID)
	if err != nil || current.Status != domain.StatusNeedsReconnect || current.Version != 2 {
		t.Fatalf("connection before reconnect = %+v %v", current, err)
	}
	restored, err := current.Reconnect(domain.ProviderAccount{ID: "account-1", Name: "renamed@example.com"}, app.Scopes, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	restored.EncryptedCredentials = sealed("grant-reconnected")
	saved, err := store.CompleteReconnect(ctx, reconnectState, restored, current.Version, testEvent("connections.connection.reconnected.v1", now))
	if err != nil || saved.Version != 3 || saved.Status != domain.StatusActive {
		t.Fatalf("CompleteReconnect() = %+v %v", saved, err)
	}
	if reloaded, err := store.Connection(ctx, org, first.ID); err != nil || reloaded.Status != domain.StatusActive ||
		reloaded.ProviderAccount.Name != "renamed@example.com" || string(reloaded.EncryptedCredentials.Ciphertext) != "ciphertext-grant-reconnected" {
		t.Fatalf("connection after reconnect = %+v %v", reloaded, err)
	}
	staleState := startSession("stale", "user-1", domain.IntentReconnect, first.ID)
	if _, err := store.CompleteReconnect(ctx, staleState, restored, current.Version, testEvent("connections.connection.reconnected.v1", now)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale CompleteReconnect() error = %v", err)
	}

	if _, err := store.DisconnectConnection(ctx, org, other.ID, "gone", now, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DisconnectConnection() with an unknown reason error = %v", err)
	}
	disconnectEvents := 0
	disconnectEvent := func(connection domain.Connection) (domain.Event, error) {
		disconnectEvents++
		if connection.ID != other.ID || connection.Status != domain.StatusDisconnected || connection.Reason != "disconnected" ||
			connection.Default || connection.Version != 3 {
			t.Errorf("disconnected connection = %+v", connection)
		}
		return testEvent("connections.connection.disconnected.v1", now), nil
	}
	for attempt, wantChanged := range []bool{true, false} {
		changed, err := store.DisconnectConnection(ctx, org, other.ID, domain.DisconnectRequested, now.Add(2*time.Minute), disconnectEvent)
		if err != nil || changed != wantChanged {
			t.Fatalf("DisconnectConnection() attempt %d = %v, %v", attempt+1, changed, err)
		}
	}
	if disconnectEvents != 1 {
		t.Fatalf("disconnect recorded %d events", disconnectEvents)
	}
	if ended, err := store.Connection(ctx, org, other.ID); err != nil || ended.Status != domain.StatusDisconnected || ended.Default ||
		ended.EncryptedCredentials == nil {
		t.Fatalf("Connection() after disconnect = %+v %v", ended, err)
	}
	if listed, err := store.Connections(ctx, org, domain.ConnectionQuery{MemberID: "user-admin", Admin: true}); err != nil || len(listed) != 2 {
		t.Fatalf("Connections() after disconnect = %+v %v", listed, err)
	}

	disabled := updated
	disabled.Status, disabled.Revision = domain.AppStatusDisabled, 3
	if err := store.UpdateApp(ctx, disabled, 2, "", testEvent("connections.app.disabled.v1", now), nil); err != nil {
		t.Fatalf("disable UpdateApp() error = %v", err)
	}
	if blocked, err := store.Connection(ctx, org, first.ID); err != nil || blocked.Blocked != "app_disabled" {
		t.Fatalf("connection of a disabled app = %+v %v", blocked, err)
	}

	disconnected := 0
	err = store.DeleteApp(ctx, org, app.ID, now.Add(3*time.Minute), testEvent("connections.app.deleted.v1", now),
		func(connection domain.Connection) (domain.Event, error) {
			disconnected++
			if connection.Status != domain.StatusDisconnected || connection.Reason != "app_deleted" || connection.Default {
				t.Errorf("connection after app deletion = %s/%s default=%v", connection.Status, connection.Reason, connection.Default)
			}
			return testEvent("connections.connection.disconnected.v1", now), nil
		})
	if err != nil || disconnected != 2 {
		t.Fatalf("DeleteApp() disconnected %d, error = %v", disconnected, err)
	}
	if _, err := store.App(ctx, org, app.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("App() after deletion error = %v", err)
	}
	if listed, err := store.Connections(ctx, org, domain.ConnectionQuery{MemberID: "user-admin", Admin: true}); err != nil || len(listed) != 0 {
		t.Fatalf("Connections() after deletion = %+v %v", listed, err)
	}

	// Created 1 + connected 3 + client change 1 + needs-reconnect 3 + reconnected 1 +
	// disconnected 1 + disabled 1 + deleted 1 + disconnected by the deletion 2.
	claimed, err := store.ClaimOutbox(ctx, 100, "claim-token")
	if err != nil || len(claimed) != 14 {
		t.Fatalf("ClaimOutbox() = %d events, error = %v", len(claimed), err)
	}
	if again, err := store.ClaimOutbox(ctx, 100, "claim-token"); err != nil || len(again) != 0 {
		t.Fatalf("second ClaimOutbox() = %d events, error = %v", len(again), err)
	}
	if marked, err := store.MarkOutboxPublished(ctx, claimed[0].ID, "claim-token"); err != nil || !marked {
		t.Fatalf("MarkOutboxPublished() = %v, %v, want true", marked, err)
	}
	// The other 13 events are still held by the live claim just taken, so a
	// different token owns none of them.
	if marked, err := store.MarkOutboxPublished(ctx, claimed[1].ID, "someone-elses-claim"); err != nil || marked {
		t.Fatalf("MarkOutboxPublished() under a foreign claim = %v, %v, want false", marked, err)
	}
	if marked, err := store.MarkOutboxFailed(ctx, claimed[1].ID, "claim-token", time.Minute, "throttled"); err != nil || !marked {
		t.Fatalf("MarkOutboxFailed() = %v, %v, want true", marked, err)
	}
	// A failed event is held back for its retry delay before it is claimable
	// again, and then it is claimable by anyone rather than by its old claim.
	if again, err := store.ClaimOutbox(ctx, 100, "second-claim"); err != nil || len(again) != 0 {
		t.Fatalf("ClaimOutbox() during the retry delay = %d events, error = %v, want 0", len(again), err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE connections_outbox SET available_at = now() - interval '1 second' WHERE id = $1`, claimed[1].ID); err != nil {
		t.Fatal(err)
	}
	again, err := store.ClaimOutbox(ctx, 100, "second-claim")
	if err != nil || len(again) != 1 || again[0].ID != claimed[1].ID || again[0].Attempts != 1 {
		t.Fatalf("ClaimOutbox() after the retry delay = %+v, error = %v, want just %s at 1 attempt", again, err, claimed[1].ID)
	}
}

// TestOutboxRejectsAnEventWithNoDetail checks that an event carrying no valid
// JSON detail is refused before it reaches the database. Left to PostgreSQL, a
// nil detail violates the column's NOT NULL and an empty one is invalid JSON:
// both abort the whole business transaction, and neither code is one
// mapWriteError recognises, so the caller would see an internal failure rather
// than a refused request.
func TestOutboxRejectsAnEventWithNoDetail(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, testCase := range []struct {
		name string
		data json.RawMessage
	}{
		{"no detail", nil},
		{"empty detail", json.RawMessage{}},
		{"malformed detail", json.RawMessage(`{"unterminated":`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := domain.App{
				ID: "app_" + randomHex(4), OrganizationID: "org_outbox_detail", Provider: "google",
				CredentialType: domain.CredentialOAuth2, ClientID: "client-1", EncryptedSecret: sealed("secret"),
				Scopes: []string{"email"}, Status: domain.AppStatusActive, Revision: 1,
				CreatedBy: "user-admin", CreatedAt: now, UpdatedAt: now,
			}
			event := testEvent("connections.app.created.v1", now)
			event.Data = testCase.data
			err := store.CreateApp(ctx, app, event)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("CreateApp() with %s error = %v, want ErrInvalid", testCase.name, err)
			}
			// The whole transaction must have rolled back: no app, no event.
			if _, err := store.App(ctx, app.OrganizationID, app.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("app survived the refused event: %v", err)
			}
			if claimed, err := store.ClaimOutbox(ctx, 10, "claim-token"); err != nil || len(claimed) != 0 {
				t.Fatalf("ClaimOutbox() = %d events, %v, want 0", len(claimed), err)
			}
		})
	}

	// A well-formed detail still works, so the guard is not refusing everything.
	app := domain.App{
		ID: "app_" + randomHex(4), OrganizationID: "org_outbox_detail", Provider: "google",
		CredentialType: domain.CredentialOAuth2, ClientID: "client-1", EncryptedSecret: sealed("secret"),
		Scopes: []string{"email"}, Status: domain.AppStatusActive, Revision: 1,
		CreatedBy: "user-admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateApp(ctx, app, testEvent("connections.app.created.v1", now)); err != nil {
		t.Fatalf("CreateApp() with a valid detail error = %v", err)
	}
}

// TestDeletePublishedOutboxKeepsPendingAndRecentEvents checks the sweep that
// stops the outbox growing once per connection change for ever. It must remove
// only events that actually reached the bus and are past the retention window:
// a pending event has not been delivered, and a recent published one is still
// useful for explaining a delivery.
func TestDeletePublishedOutboxKeepsPendingAndRecentEvents(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Three events: one published long ago, one published just now, one never
	// published.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_outbox (id, detail_type, detail, occurred_at, published_at) VALUES
			('evt_old',    'connections.app.created.v1', '{}', $1, $2),
			('evt_recent', 'connections.app.created.v1', '{}', $1, $1),
			('evt_pending','connections.app.created.v1', '{}', $1, NULL)`,
		now, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeletePublishedOutbox(ctx, now.Add(-7*24*time.Hour), 500)
	if err != nil {
		t.Fatalf("DeletePublishedOutbox() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeletePublishedOutbox() deleted %d, want 1", deleted)
	}

	remaining := map[string]bool{}
	rows, err := store.pool.Query(ctx, `SELECT id FROM connections_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining[id] = true
	}
	if remaining["evt_old"] {
		t.Error("a published event past the retention window was kept")
	}
	if !remaining["evt_recent"] {
		t.Error("a recently published event was removed")
	}
	if !remaining["evt_pending"] {
		t.Error("an undelivered event was removed")
	}

	// The sweep is bounded, so a large backlog is worked off over runs.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_outbox (id, detail_type, detail, occurred_at, published_at)
		SELECT 'evt_bulk_'||g, 'connections.app.created.v1', '{}', $1, $2
		FROM generate_series(0, 9) g`, now, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeletePublishedOutbox(ctx, now.Add(-7*24*time.Hour), 4); err != nil || deleted != 4 {
		t.Fatalf("bounded DeletePublishedOutbox() deleted %d, %v, want 4", deleted, err)
	}
}

// TestOutboxClaimOwnershipSurvivesATakeover covers the claim TTL gap. A
// dispatcher claims a batch, then stalls past the claim's life; another
// replica's claim becomes available and it takes the events over. When the
// first dispatcher finally marks its events, those marks must not touch rows
// the second claim now owns — otherwise the stale dispatcher clears the live
// claim and a third replica can claim and publish the same event again.
func TestOutboxClaimOwnershipSurvivesATakeover(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO connections_outbox (id, detail_type, detail, occurred_at)
		VALUES ('evt_takeover', 'connections.app.created.v1', '{}', $1)`, now); err != nil {
		t.Fatal(err)
	}

	first, err := store.ClaimOutbox(ctx, 10, "claim-one")
	if err != nil || len(first) != 1 || first[0].ClaimToken != "claim-one" {
		t.Fatalf("first ClaimOutbox() = %+v, %v", first, err)
	}
	// A second claim cannot see it while the first is live.
	if second, err := store.ClaimOutbox(ctx, 10, "claim-two"); err != nil || len(second) != 0 {
		t.Fatalf("ClaimOutbox() while claimed = %d events, %v, want 0", len(second), err)
	}

	// Stall past the claim's life, as a dispatcher that hangs would.
	if _, err := store.pool.Exec(ctx,
		`UPDATE connections_outbox SET locked_until = now() - interval '1 second' WHERE id = 'evt_takeover'`); err != nil {
		t.Fatal(err)
	}
	taken, err := store.ClaimOutbox(ctx, 10, "claim-two")
	if err != nil || len(taken) != 1 {
		t.Fatalf("ClaimOutbox() after the claim expired = %d events, %v, want 1", len(taken), err)
	}

	// The stalls' marks are refused: they no longer own the row.
	if marked, err := store.MarkOutboxPublished(ctx, "evt_takeover", "claim-one"); err != nil || marked {
		t.Fatalf("stale MarkOutboxPublished() = %v, %v, want false", marked, err)
	}
	if marked, err := store.MarkOutboxFailed(ctx, "evt_takeover", "claim-one", time.Minute, "stale"); err != nil || marked {
		t.Fatalf("stale MarkOutboxFailed() = %v, %v, want false", marked, err)
	}
	// The live claim still holds it, so the stall did not hand it to a third
	// dispatcher.
	if third, err := store.ClaimOutbox(ctx, 10, "claim-three"); err != nil || len(third) != 0 {
		t.Fatalf("ClaimOutbox() after a stale mark = %d events, %v, want 0", len(third), err)
	}
	// The owner can still finish.
	if marked, err := store.MarkOutboxPublished(ctx, "evt_takeover", "claim-two"); err != nil || !marked {
		t.Fatalf("owner MarkOutboxPublished() = %v, %v, want true", marked, err)
	}
}

// TestOpenRequiredExtensions covers Open's requiredExtensions parameter,
// which POSTGRES_REQUIRED_EXTENSIONS (see internal/host) passes through to
// postgreskit.Open: it succeeds when every named extension is installed, and
// refuses, naming it, when one is not. openTestStore always opens with none,
// so nothing else in this package exercises it.
func TestOpenRequiredExtensions(t *testing.T) {
	databaseURL := os.Getenv("CONNECTIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CONNECTIONS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	// pg_trgm ships with every standard PostgreSQL install and, since
	// PostgreSQL 13, is trusted: a role that can CREATE SCHEMA (which
	// openTestStore already relies on) can install it without being
	// superuser. If this environment still refuses it, skip rather than fail,
	// since that is a property of the database, not of Open.
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		t.Skipf("pg_trgm could not be installed on this database: %v", err)
	}

	store, err := Open(ctx, databaseURL, "pg_trgm")
	if err != nil {
		t.Fatalf("Open() with an installed extension error = %v", err)
	}
	store.Close()

	if _, err := Open(ctx, databaseURL, "pg_trgm", "not_a_real_extension"); err == nil || !strings.Contains(err.Error(), "not_a_real_extension") {
		t.Fatalf("Open() with a missing extension error = %v, want it to name not_a_real_extension", err)
	}
}
