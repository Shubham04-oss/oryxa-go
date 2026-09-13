package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestStoreMemberConnectionLifecycle(t *testing.T) {
	databaseURL := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
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
	organizationID := "it-org-" + suffix
	profileID := "it-app-" + suffix
	memberID := "it-member-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_outbox WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_connections WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_oauth_apps WHERE organization_id = $1`, organizationID)
	}()

	profile := domain.OAuthAppProfile{
		ID: profileID, OrganizationID: organizationID, Provider: "slack",
		PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
		Mode: domain.OAuthAppModeBYOC, ClientID: "safe-client-id",
		EncryptedSecret: domain.SealedData{
			Version: 1, Nonce: []byte{1, 2}, Ciphertext: []byte{3, 4}, EncryptedDataKey: []byte{5, 6},
		},
		Scopes: []string{"channels:history", "groups:history"}, Status: domain.OAuthAppStatusActive,
		CreatedBy: "admin-1", CreatedAt: now, UpdatedAt: now, CredentialRevision: 1,
	}
	if err := store.CreateOAuthApp(ctx, profile); err != nil {
		t.Fatalf("create OAuth app: %v", err)
	}
	disabledProfile := profile
	disabledProfile.ID = "it-disabled-app-" + suffix
	disabledProfile.Status = domain.OAuthAppStatusDisabled
	if err := store.CreateOAuthApp(ctx, disabledProfile); err != nil {
		t.Fatalf("create disabled OAuth app: %v", err)
	}
	loadedProfile, err := store.OAuthApp(ctx, organizationID, profileID)
	if err != nil || loadedProfile.PieceVersion != "0.17.10" || loadedProfile.EncryptedSecret.Version != 1 {
		t.Fatalf("loaded profile/error = %+v/%v", loadedProfile, err)
	}

	available, err := store.ListMemberIntegrations(ctx, organizationID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].State != domain.MemberIntegrationAvailable || available[0].Connection != nil {
		t.Fatalf("catalog before connection = %+v", available)
	}

	connection := domain.Connection{
		ID: "it-connection-" + suffix, OrganizationID: organizationID,
		MemberID: memberID, ProfileID: profileID, Provider: "slack",
		PieceName: profile.PieceName, PieceVersion: profile.PieceVersion,
		Identity: domain.ProviderIdentity{
			SubjectID: "U123", AccountID: "T123", AccountName: "Oryxa",
			TenantID: "T123", TenantName: "Oryxa",
		},
		GrantedScopes: []string{"channels:history", "groups:history"},
		EncryptedCredentials: domain.SealedData{
			Version: 1, Nonce: []byte{7, 8}, Ciphertext: []byte{9, 10}, EncryptedDataKey: []byte{11, 12},
		},
		Status: domain.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	event := domain.Event{
		EventID: "it-event-" + suffix, Source: integrationevents.Source,
		DetailType: integrationevents.ConnectionConnectedDetailType, Version: 1,
		OrganizationID: organizationID, OccurredAt: now,
		Data: integrationevents.ConnectionConnected{
			ConnectionID: connection.ID, MemberID: memberID, ProfileID: profileID,
			Provider: "slack", Identity: integrationevents.ProviderIdentity{SubjectID: "U123", TenantID: "T123"},
			Scopes: connection.GrantedScopes, ConnectedAt: now,
		},
	}
	if err := store.CreateConnectionWithEvent(ctx, connection, event); err != nil {
		t.Fatalf("create connection and event: %v", err)
	}

	connected, err := store.ListMemberIntegrations(ctx, organizationID, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(connected) != 1 || connected[0].State != domain.MemberIntegrationConnected || connected[0].Connection == nil || connected[0].Connection.ID != connection.ID {
		t.Fatalf("catalog after connection = %+v", connected)
	}
	otherMember, err := store.ListMemberIntegrations(ctx, organizationID, "another-member")
	if err != nil || len(otherMember) != 1 || otherMember[0].State != domain.MemberIntegrationAvailable {
		t.Fatalf("other member catalog/error = %+v/%v", otherMember, err)
	}

	full, err := store.ConnectionForExecution(ctx, organizationID, connection.ID)
	if err != nil || full.EncryptedCredentials.Version != 1 || len(full.EncryptedCredentials.Ciphertext) == 0 {
		t.Fatalf("broker connection/error = %+v/%v", full, err)
	}
	catalog, err := store.ConnectionCatalog(ctx, organizationID)
	if err != nil || len(catalog) != 1 || catalog[0].ConnectionID != connection.ID {
		t.Fatalf("safe connection catalog/error = %+v/%v", catalog, err)
	}
	safeJSON, _ := json.Marshal(struct {
		Member  []domain.MemberIntegration      `json:"member"`
		Catalog []domain.ConnectionCatalogEntry `json:"catalog"`
	}{connected, catalog})
	for _, forbidden := range []string{"encrypted_credentials", "encrypted_data_key", "ciphertext", "access_token", "refresh_token"} {
		if strings.Contains(string(safeJSON), forbidden) {
			t.Fatalf("safe catalog contains %q: %s", forbidden, safeJSON)
		}
	}
	var forbiddenViewColumns int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'integrations_connection_catalog'
		  AND column_name = ANY($1)`, []string{
		"encrypted_credentials", "encrypted_data_key", "ciphertext",
		"access_token", "refresh_token", "client_secret",
	}).Scan(&forbiddenViewColumns); err != nil || forbiddenViewColumns != 0 {
		t.Fatalf("safe view exposes forbidden columns: count=%d error=%v", forbiddenViewColumns, err)
	}

	var outboxDetail string
	if err := store.pool.QueryRow(ctx, `SELECT detail::text FROM integrations_outbox WHERE id = $1`, event.EventID).Scan(&outboxDetail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outboxDetail, "encrypted") || strings.Contains(outboxDetail, "token") {
		t.Fatalf("outbox contains credentials: %s", outboxDetail)
	}

	duplicate := connection
	duplicate.ID = "it-duplicate-" + suffix
	duplicateEvent := event
	duplicateEvent.EventID = "it-duplicate-event-" + suffix
	duplicateEvent.Data = integrationevents.ConnectionConnected{ConnectionID: duplicate.ID}
	if err := store.CreateConnectionWithEvent(ctx, duplicate, duplicateEvent); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate active connection error = %v, want conflict", err)
	}
	var duplicateEventCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM integrations_outbox WHERE id = $1`, duplicateEvent.EventID).Scan(&duplicateEventCount); err != nil || duplicateEventCount != 0 {
		t.Fatalf("duplicate transaction wrote event: count=%d error=%v", duplicateEventCount, err)
	}

	rolledBack := connection
	rolledBack.ID = "it-rolled-back-" + suffix
	rolledBack.MemberID = "another-member"
	if err := store.CreateConnectionWithEvent(ctx, rolledBack, event); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate event error = %v, want conflict", err)
	}
	if _, err := store.ConnectionForExecution(ctx, organizationID, rolledBack.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("connection insert did not roll back with event: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE integrations_connections
		SET status = 'disconnected', updated_at = now(), version = version + 1
		WHERE id = $1`, connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConnectionForExecution(ctx, organizationID, connection.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disconnected credential remained executable: %v", err)
	}
	againAvailable, err := store.ListMemberIntegrations(ctx, organizationID, memberID)
	if err != nil || len(againAvailable) != 1 || againAvailable[0].State != domain.MemberIntegrationAvailable {
		t.Fatalf("catalog after disconnect/error = %+v/%v", againAvailable, err)
	}
}
