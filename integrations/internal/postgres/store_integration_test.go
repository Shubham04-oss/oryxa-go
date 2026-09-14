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

func TestMigrateSerializesConcurrentCallers(t *testing.T) {
	databaseURL := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func(store *Store) {
			<-start
			results <- store.Migrate(ctx)
		}(store)
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
}

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

func TestStorePersistsMicrosoftGraphCleanupWithConnectionAndProfileDisable(t *testing.T) {
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
	organizationID := "cleanup-org-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_microsoft_graph_cleanup WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_triggers WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_outbox WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_connections WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_oauth_apps WHERE organization_id = $1`, organizationID)
	}()

	create := func(profileID, connectionID, triggerID, triggerName string) {
		profile := domain.OAuthAppProfile{ID: profileID, OrganizationID: organizationID, Provider: "microsoft",
			PieceName: "@activepieces/piece-microsoft-outlook", PieceVersion: "0.5.3", Mode: domain.OAuthAppModeBYOC,
			ClientID: "client", EncryptedSecret: testSealed(1), Scopes: []string{"Mail.Read"}, Status: domain.OAuthAppStatusActive,
			CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, CredentialRevision: 1}
		if err := store.CreateOAuthApp(ctx, profile); err != nil {
			t.Fatalf("create profile: %v", err)
		}
		connection := domain.Connection{ID: connectionID, OrganizationID: organizationID, MemberID: "member", ProfileID: profileID,
			Provider: "microsoft", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion,
			Identity: domain.ProviderIdentity{SubjectID: "subject", TenantID: "tenant"}, GrantedScopes: []string{"Mail.Read"},
			EncryptedCredentials: testSealed(2), Status: domain.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
		connected := domain.Event{EventID: "connected-" + triggerID, Source: integrationevents.Source, DetailType: integrationevents.ConnectionConnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionConnected{ConnectionID: connectionID}}
		if err := store.CreateConnectionWithEvent(ctx, connection, connected); err != nil {
			t.Fatalf("create connection: %v", err)
		}
		trigger := domain.Trigger{ID: triggerID, OrganizationID: organizationID, MemberID: "member", ConnectionID: connectionID, ProfileID: profileID,
			Provider: "microsoft", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion, TriggerName: triggerName,
			ProviderIdentifier: connectionID, Status: domain.TriggerStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
		enabled := domain.Event{EventID: "enabled-" + triggerID, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: triggerID, ConnectionID: connectionID}}
		if err := store.CreateTriggerWithEvent(ctx, trigger, enabled); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
	}

	profileOne, connectionOne := "cleanup-profile-one-"+suffix, "cleanup-connection-one-"+suffix
	profileTwo, connectionTwo := "cleanup-profile-two-"+suffix, "cleanup-connection-two-"+suffix
	create(profileOne, connectionOne, "cleanup-trigger-email-"+suffix, "new-email")
	create(profileTwo, connectionTwo, "cleanup-trigger-calendar-"+suffix, "calendar-event-changed")
	// A Microsoft action/polling trigger name is disabled locally but is never
	// queued for Graph subscription deletion.
	trigger := domain.Trigger{ID: "cleanup-trigger-other-" + suffix, OrganizationID: organizationID, MemberID: "member", ConnectionID: connectionOne, ProfileID: profileOne,
		Provider: "microsoft", PieceName: "@activepieces/piece-microsoft-outlook", PieceVersion: "0.5.3", TriggerName: "not-a-graph-trigger", ProviderIdentifier: connectionOne,
		Status: domain.TriggerStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTriggerWithEvent(ctx, trigger, domain.Event{EventID: "enabled-other-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: trigger.ID, ConnectionID: connectionOne}}); err != nil {
		t.Fatal(err)
	}

	if err := store.DisconnectConnectionWithEvent(ctx, organizationID, "member", connectionOne, now, domain.Event{EventID: "disconnected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionDisconnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionDisconnected{ConnectionID: connectionOne}}); err != nil {
		t.Fatalf("disconnect connection: %v", err)
	}
	if err := store.DisableOAuthAppWithEvent(ctx, organizationID, profileTwo, now, domain.Event{EventID: "profile-disabled-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.OAuthAppDisabledDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.OAuthAppDisabled{ProfileID: profileTwo}}); err != nil {
		t.Fatalf("disable profile: %v", err)
	}
	rows, err := store.pool.Query(ctx, `SELECT connection_id, profile_id, trigger_name FROM integrations_microsoft_graph_cleanup WHERE organization_id = $1 ORDER BY trigger_name`, organizationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var jobs [][3]string
	for rows.Next() {
		var job [3]string
		if err := rows.Scan(&job[0], &job[1], &job[2]); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0][0] != connectionTwo || jobs[0][1] != profileTwo || jobs[0][2] != "calendar-event-changed" ||
		jobs[1][0] != connectionOne || jobs[1][1] != profileOne || jobs[1][2] != "new-email" {
		t.Fatalf("cleanup jobs = %#v", jobs)
	}
	claimed, err := store.ClaimMicrosoftGraphCleanup(ctx, 10, "lease-one-"+suffix, now, now.Add(time.Minute))
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim cleanup jobs = %#v/%v", claimed, err)
	}
	var emailJob, calendarJob domain.MicrosoftGraphCleanupJob
	for _, job := range claimed {
		if job.OrganizationID != organizationID || job.Connection.OrganizationID != organizationID || job.Connection.EncryptedCredentials.Version == 0 {
			t.Fatalf("claimed job crossed tenant or lost sealed grant: %+v", job)
		}
		switch job.TriggerName {
		case "new-email":
			emailJob = job
		case "calendar-event-changed":
			calendarJob = job
		}
	}
	wrongTenant := emailJob
	wrongTenant.OrganizationID = "other-org"
	if err := store.CompleteMicrosoftGraphCleanup(ctx, wrongTenant, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-tenant complete error = %v, want conflict", err)
	}
	if err := store.RetryMicrosoftGraphCleanup(ctx, emailJob, now, "cleanup_failed"); err != nil {
		t.Fatalf("retry cleanup job: %v", err)
	}
	// The calendar job remains protected by its first lease. Only the retried
	// email job is claimable, proving retry and lease isolation.
	reclaimed, err := store.ClaimMicrosoftGraphCleanup(ctx, 10, "lease-two-"+suffix, now, now.Add(time.Minute))
	if err != nil || len(reclaimed) != 1 || reclaimed[0].TriggerID != emailJob.TriggerID || reclaimed[0].Attempt != 2 {
		t.Fatalf("reclaim cleanup job = %#v/%v", reclaimed, err)
	}
	if err := store.CompleteMicrosoftGraphCleanup(ctx, reclaimed[0], now); err != nil {
		t.Fatalf("complete retried cleanup job: %v", err)
	}
	if err := store.CompleteMicrosoftGraphCleanup(ctx, calendarJob, now); err != nil {
		t.Fatalf("complete leased cleanup job: %v", err)
	}
	empty, err := store.ClaimMicrosoftGraphCleanup(ctx, 10, "lease-three-"+suffix, now.Add(2*time.Minute), now.Add(3*time.Minute))
	if err != nil || len(empty) != 0 {
		t.Fatalf("completed cleanup jobs reclaimed = %#v/%v", empty, err)
	}

	// A permanent OAuth refresh rejection follows the same durable teardown
	// path as an explicit disconnect. The cleanup instruction and trigger
	// disable must commit with the reauthorization transition.
	profileThree, connectionThree, triggerThree := "cleanup-profile-three-"+suffix, "cleanup-connection-three-"+suffix, "cleanup-trigger-reauth-"+suffix
	create(profileThree, connectionThree, triggerThree, "new-email")
	refreshLease := "refresh-reauth-" + suffix
	if acquired, err := store.AcquireConnectionRefreshLease(ctx, organizationID, connectionThree, 1, refreshLease, now, now.Add(time.Minute)); err != nil || !acquired {
		t.Fatalf("acquire reauthorization lease = %v/%v", acquired, err)
	}
	reauthorization := domain.Event{EventID: "reauthorization-" + suffix, Source: integrationevents.Source,
		DetailType: integrationevents.ConnectionReauthorizationRequiredDetailType, Version: 1,
		OrganizationID: organizationID, OccurredAt: now,
		Data: integrationevents.ConnectionReauthorizationRequired{ConnectionID: connectionThree, Provider: "microsoft", Reason: "invalid_grant", RequiredAt: now}}
	if err := store.MarkConnectionReauthorizationWithEvent(ctx, organizationID, connectionThree, refreshLease, 1, now, reauthorization); err != nil {
		t.Fatalf("mark connection for reauthorization: %v", err)
	}
	var cleanupCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM integrations_microsoft_graph_cleanup WHERE organization_id=$1 AND connection_id=$2 AND trigger_id=$3 AND completed_at IS NULL`, organizationID, connectionThree, triggerThree).Scan(&cleanupCount); err != nil || cleanupCount != 1 {
		t.Fatalf("reauthorization cleanup row = %d/%v", cleanupCount, err)
	}
	loaded, err := store.Trigger(ctx, organizationID, triggerThree)
	if err != nil || loaded.Status != domain.TriggerStatusDisabled {
		t.Fatalf("reauthorization trigger state = %+v/%v", loaded, err)
	}

	profileFour, connectionFour, triggerFour := "cleanup-profile-four-"+suffix, "cleanup-connection-four-"+suffix, "cleanup-trigger-disabled-"+suffix
	create(profileFour, connectionFour, triggerFour, "calendar-event-changed")
	manualDisable := domain.Event{EventID: "manual-disable-" + suffix, Source: integrationevents.Source,
		DetailType: integrationevents.TriggerDisabledDetailType, Version: 1,
		OrganizationID: organizationID, OccurredAt: now,
		Data: integrationevents.TriggerDisabled{TriggerID: triggerFour, ConnectionID: connectionFour, Provider: "microsoft", DisabledBy: "member", DisabledAt: now}}
	if err := store.DisableTriggerWithEvent(ctx, organizationID, "member", triggerFour, now, manualDisable); err != nil {
		t.Fatalf("disable Microsoft Graph trigger: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM integrations_microsoft_graph_cleanup WHERE organization_id=$1 AND connection_id=$2 AND trigger_id=$3 AND completed_at IS NULL`, organizationID, connectionFour, triggerFour).Scan(&cleanupCount); err != nil || cleanupCount != 1 {
		t.Fatalf("manual disable cleanup row = %d/%v", cleanupCount, err)
	}
}

func TestStorePersistsGitHubCleanupOnDisconnect(t *testing.T) {
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
	organizationID := "github-cleanup-org-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_github_hook_cleanup WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_triggers WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_outbox WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_connections WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_oauth_apps WHERE organization_id = $1`, organizationID)
	}()

	profileID, connectionID, triggerID := "gh-profile-"+suffix, "gh-connection-"+suffix, "gh-trigger-"+suffix
	profile := domain.OAuthAppProfile{ID: profileID, OrganizationID: organizationID, Provider: "github",
		PieceName: "@activepieces/piece-github", PieceVersion: "0.8.11", Mode: domain.OAuthAppModeBYOC,
		ClientID: "client", EncryptedSecret: testSealed(1), Scopes: []string{"repo", "admin:repo_hook"}, Status: domain.OAuthAppStatusActive,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, CredentialRevision: 1}
	if err := store.CreateOAuthApp(ctx, profile); err != nil {
		t.Fatal(err)
	}
	connection := domain.Connection{ID: connectionID, OrganizationID: organizationID, MemberID: "member", ProfileID: profileID,
		Provider: "github", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion,
		Identity: domain.ProviderIdentity{SubjectID: "1", AccountID: "oryxa"}, GrantedScopes: []string{"repo"},
		EncryptedCredentials: testSealed(2), Status: domain.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
	if err := store.CreateConnectionWithEvent(ctx, connection, domain.Event{EventID: "connected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionConnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionConnected{ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	trigger := domain.Trigger{ID: triggerID, OrganizationID: organizationID, MemberID: "member", ConnectionID: connectionID, ProfileID: profileID,
		Provider: "github", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion, TriggerName: "new-issue",
		ProviderIdentifier: connectionID, Status: domain.TriggerStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTriggerWithEvent(ctx, trigger, domain.Event{EventID: "enabled-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: triggerID, ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DisconnectConnectionWithEvent(ctx, organizationID, "member", connectionID, now, domain.Event{EventID: "disconnected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionDisconnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionDisconnected{ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimGitHubCleanup(ctx, 10, "lease-"+suffix, now, now.Add(time.Minute))
	if err != nil || len(claimed) != 1 || claimed[0].TriggerID != triggerID || claimed[0].TriggerName != "new-issue" ||
		claimed[0].Connection.Provider != "github" || claimed[0].Connection.EncryptedCredentials.Version == 0 {
		t.Fatalf("claimed GitHub cleanup = %#v/%v", claimed, err)
	}
	if err := store.CompleteGitHubCleanup(ctx, claimed[0], now); err != nil {
		t.Fatal(err)
	}
}

func TestStorePersistsGoogleCleanupOnDisconnect(t *testing.T) {
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
	organizationID := "google-cleanup-org-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_google_watch_cleanup WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_triggers WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_outbox WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_connections WHERE organization_id = $1`, organizationID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM integrations_oauth_apps WHERE organization_id = $1`, organizationID)
	}()

	profileID, connectionID, triggerID := "g-profile-"+suffix, "g-connection-"+suffix, "g-trigger-"+suffix
	profile := domain.OAuthAppProfile{ID: profileID, OrganizationID: organizationID, Provider: "google",
		PieceName: "@activepieces/piece-gmail", PieceVersion: "0.15.0", Mode: domain.OAuthAppModeBYOC,
		ClientID: "client", EncryptedSecret: testSealed(1), Scopes: []string{"https://www.googleapis.com/auth/gmail.metadata"}, Status: domain.OAuthAppStatusActive,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now, CredentialRevision: 1}
	if err := store.CreateOAuthApp(ctx, profile); err != nil {
		t.Fatal(err)
	}
	connection := domain.Connection{ID: connectionID, OrganizationID: organizationID, MemberID: "member", ProfileID: profileID,
		Provider: "google", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion,
		Identity: domain.ProviderIdentity{SubjectID: "1", AccountID: "member@example.com"}, GrantedScopes: []string{"https://www.googleapis.com/auth/gmail.metadata"},
		EncryptedCredentials: testSealed(2), Status: domain.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
	if err := store.CreateConnectionWithEvent(ctx, connection, domain.Event{EventID: "connected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionConnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionConnected{ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	trigger := domain.Trigger{ID: triggerID, OrganizationID: organizationID, MemberID: "member", ConnectionID: connectionID, ProfileID: profileID,
		Provider: "google", PieceName: profile.PieceName, PieceVersion: profile.PieceVersion, TriggerName: "new-email",
		ProviderIdentifier: connectionID, Status: domain.TriggerStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTriggerWithEvent(ctx, trigger, domain.Event{EventID: "enabled-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: triggerID, ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DisconnectConnectionWithEvent(ctx, organizationID, "member", connectionID, now, domain.Event{EventID: "disconnected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionDisconnectedDetailType, Version: 1, OrganizationID: organizationID, OccurredAt: now, Data: integrationevents.ConnectionDisconnected{ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimGoogleCleanup(ctx, 10, "lease-"+suffix, now, now.Add(time.Minute))
	if err != nil || len(claimed) != 1 || claimed[0].TriggerID != triggerID || claimed[0].TriggerName != "new-email" ||
		claimed[0].Connection.Provider != "google" || claimed[0].Connection.EncryptedCredentials.Version == 0 {
		t.Fatalf("claimed Google cleanup = %#v/%v", claimed, err)
	}
	if err := store.CompleteGoogleCleanup(ctx, claimed[0], now); err != nil {
		t.Fatal(err)
	}
}
