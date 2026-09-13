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
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

func TestStoreTenantAuthorizationAndTriggerExecution(t *testing.T) {
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

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	org := "exec-org-" + suffix
	otherOrg := "exec-other-org-" + suffix
	admin := "exec-admin-" + suffix
	member := "exec-member-" + suffix
	outsider := "exec-outsider-" + suffix
	profileID := "exec-app-" + suffix
	connectionID := "exec-con-" + suffix
	triggerID := "exec-trg-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_outbox WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_trigger_executions WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_triggers WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_connections WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_oauth_apps WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM auth_memberships WHERE organization_id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM auth_organizations WHERE id IN ($1,$2)`, org, otherOrg)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM auth_users WHERE id = ANY($1)`, []string{admin, member, outsider})
	}()
	for _, user := range []string{admin, member, outsider} {
		if _, err := store.pool.Exec(ctx, `INSERT INTO auth_users (id,email,created_at,updated_at) VALUES ($1,$2,$3,$3)`, user, user+"@example.test", now); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO auth_organizations (id,name,slug,created_by,created_at,updated_at) VALUES ($1,'Exec Org',$2,$3,$4,$4),($5,'Other Org',$6,$3,$4,$4)`, org, "exec-"+suffix, admin, now, otherOrg, "exec-other-"+suffix); err != nil {
		t.Fatalf("seed orgs: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO auth_memberships (organization_id,user_id,role,joined_at) VALUES ($1,$2,'admin',$4),($1,$3,'member',$4)`, org, admin, member, now); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := store.CanManageOAuthApps(ctx, org, admin); err != nil {
		t.Fatalf("admin authorization: %v", err)
	}
	if err := store.CanManageOAuthApps(ctx, org, member); !errors.Is(err, oauthflow.ErrForbidden) {
		t.Fatalf("member admin authorization: %v", err)
	}
	if err := store.CanCreateConnection(ctx, org, member); err != nil {
		t.Fatalf("member connection authorization: %v", err)
	}
	if err := store.CanCreateConnection(ctx, org, outsider); !errors.Is(err, oauthflow.ErrForbidden) {
		t.Fatalf("outsider authorization: %v", err)
	}
	if err := store.CanCreateConnection(ctx, otherOrg, member); !errors.Is(err, oauthflow.ErrForbidden) {
		t.Fatalf("cross-org authorization: %v", err)
	}

	profile := domain.OAuthAppProfile{ID: profileID, OrganizationID: org, Provider: "slack", PieceName: "piece", PieceVersion: "1", Mode: domain.OAuthAppModeBYOC, ClientID: "client", EncryptedSecret: testSealed(1), Scopes: []string{"scope"}, Status: domain.OAuthAppStatusActive, CreatedBy: admin, CreatedAt: now, UpdatedAt: now, CredentialRevision: 1}
	if err := store.CreateOAuthApp(ctx, profile); err != nil {
		t.Fatal(err)
	}
	connection := domain.Connection{ID: connectionID, OrganizationID: org, MemberID: member, ProfileID: profileID, Provider: "slack", PieceName: "piece", PieceVersion: "1", Identity: domain.ProviderIdentity{SubjectID: "U123", TenantID: "T123"}, GrantedScopes: []string{"scope"}, EncryptedCredentials: testSealed(2), Status: domain.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
	if err := store.CreateConnectionWithEvent(ctx, connection, domain.Event{EventID: "exec-connected-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.ConnectionConnectedDetailType, Version: 1, OrganizationID: org, OccurredAt: now, Data: integrationevents.ConnectionConnected{ConnectionID: connectionID}}); err != nil {
		t.Fatal(err)
	}
	trigger := domain.Trigger{ID: triggerID, OrganizationID: org, MemberID: member, ConnectionID: connectionID, ProfileID: profileID, Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", ProviderIdentifier: "T123", Props: json.RawMessage(`{"ignoreBots":true}`), Status: domain.TriggerStatusActive, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateTriggerWithEvent(ctx, trigger, domain.Event{EventID: "exec-trigger-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: org, OccurredAt: now, Data: integrationevents.TriggerEnabled{TriggerID: triggerID}}); err != nil {
		t.Fatal(err)
	}
	runnable, err := store.RunnableTriggers(ctx, org, profileID, "piece", "1", "hook", "", "T123")
	if err != nil || len(runnable) != 1 || runnable[0].ID != triggerID {
		t.Fatalf("runnable/error = %+v/%v", runnable, err)
	}
	for _, mismatch := range [][2]string{{otherOrg, "T123"}, {org, "T999"}} {
		items, err := store.RunnableTriggers(ctx, mismatch[0], profileID, "piece", "1", "hook", "", mismatch[1])
		if err != nil || len(items) != 0 {
			t.Fatalf("isolated query %v = %+v/%v", mismatch, items, err)
		}
	}
	crossTenant := trigger
	crossTenant.ID = "exec-cross-" + suffix
	crossTenant.OrganizationID = otherOrg
	if err := store.CreateTriggerWithEvent(ctx, crossTenant, domain.Event{EventID: "exec-cross-event-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.TriggerEnabledDetailType, Version: 1, OrganizationID: otherOrg, OccurredAt: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-tenant trigger FK = %v", err)
	}

	execution, claimed, err := store.ClaimTriggerExecution(ctx, "exec-id-"+suffix, org, triggerID, "delivery-1", now, now.Add(-time.Minute))
	if err != nil || !claimed || execution.Attempt != 1 {
		t.Fatalf("claim = %+v/%v/%v", execution, claimed, err)
	}
	if _, claimed, err := store.ClaimTriggerExecution(ctx, "another-id-"+suffix, org, triggerID, "delivery-1", now.Add(time.Second), now.Add(-time.Minute)); err != nil || claimed {
		t.Fatalf("concurrent claim = %v/%v", claimed, err)
	}
	outputEvent := domain.Event{EventID: "exec-output-" + suffix, Source: integrationevents.Source, DetailType: integrationevents.SlackMessageReceivedDetailType, Version: 1, OrganizationID: org, OccurredAt: now, Data: integrationevents.SlackMessageReceived{TriggerID: triggerID, ConnectionID: connectionID, WorkspaceID: "T123", Messages: []integrationevents.SlackMessage{{ChannelID: "C123", Timestamp: "1.2"}}, ReceivedAt: now}}
	if err := store.CompleteTriggerExecutionWithEvent(ctx, execution, now, outputEvent); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimTriggerExecution(ctx, "third-id-"+suffix, org, triggerID, "delivery-1", now.Add(time.Hour), now); err != nil || claimed {
		t.Fatalf("completed redelivery claim = %v/%v", claimed, err)
	}
	var detail string
	if err := store.pool.QueryRow(ctx, `SELECT detail::text FROM integrations_outbox WHERE id = $1`, outputEvent.EventID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"access_token", "encrypted", "raw_body"} {
		if strings.Contains(detail, forbidden) {
			t.Fatalf("output event contains %q: %s", forbidden, detail)
		}
	}
}

func testSealed(seed byte) domain.SealedData {
	return domain.SealedData{Version: 1, Nonce: []byte{seed}, Ciphertext: []byte{seed + 1}, EncryptedDataKey: []byte{seed + 2}}
}
