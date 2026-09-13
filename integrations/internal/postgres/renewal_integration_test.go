package postgres

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestMicrosoftGraphRenewalLeaseExcludesReplicasAndReleasesAfterCancellation(t *testing.T) {
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

	running, stop := context.WithCancel(ctx)
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := first.WithMicrosoftGraphRenewalLease(running, func(callbackCtx context.Context) error {
			close(entered)
			<-callbackCtx.Done()
			return callbackCtx.Err()
		})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first replica did not acquire renewal lease")
	}
	called := false
	acquired, err := second.WithMicrosoftGraphRenewalLease(ctx, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || acquired || called {
		t.Fatalf("second replica lease = acquired:%v called:%v err:%v", acquired, called, err)
	}
	stop()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lease error = %v", err)
	}
	acquired, err = second.WithMicrosoftGraphRenewalLease(ctx, func(context.Context) error { return nil })
	if err != nil || !acquired {
		t.Fatalf("lease was not released after cancellation: acquired:%v err:%v", acquired, err)
	}
	callbackErr := errors.New("expected callback failure")
	acquired, err = second.WithMicrosoftGraphRenewalLease(ctx, func(context.Context) error { return callbackErr })
	if !acquired || !errors.Is(err, callbackErr) {
		t.Fatalf("callback error lease = acquired:%v err:%v", acquired, err)
	}
	acquired, err = first.WithMicrosoftGraphRenewalLease(ctx, func(context.Context) error { return nil })
	if err != nil || !acquired {
		t.Fatalf("lease was not released after callback error: acquired:%v err:%v", acquired, err)
	}
}

func TestActiveMicrosoftGraphTriggersUsesStableBoundedCursor(t *testing.T) {
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
	organizations := []string{"renew-a-" + suffix, "renew-b-" + suffix}
	profiles := []string{"renew-profile-a-" + suffix, "renew-profile-b-" + suffix}
	connections := []string{"renew-connection-a-" + suffix, "renew-connection-b-" + suffix, "renew-connection-inactive-" + suffix, "renew-connection-slack-" + suffix}
	triggers := []string{"renew-trg-1-" + suffix, "renew-trg-2-" + suffix, "renew-trg-3-" + suffix, "renew-trg-disabled-" + suffix, "renew-trg-slack-" + suffix, "renew-trg-inactive-" + suffix, "renew-trg-other-microsoft-" + suffix}
	now := time.Now().UTC()
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_triggers WHERE id = ANY($1)`, triggers)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_connections WHERE id = ANY($1)`, connections)
		_, _ = store.pool.Exec(cleanup, `DELETE FROM integrations_oauth_apps WHERE id = ANY($1)`, profiles)
	}()
	for index, organizationID := range organizations {
		if _, err := store.pool.Exec(ctx, `INSERT INTO integrations_oauth_apps (
			id, organization_id, provider, piece_name, piece_version, mode, client_id,
			encrypted_secret, scopes, status, created_by, created_at, updated_at, credential_revision
		) VALUES ($1,$2,'microsoft','piece','1','byoc','client','{}','{}','active','member',$3,$3,1)`, profiles[index], organizationID, now); err != nil {
			t.Fatal(err)
		}
	}
	insertConnection := func(id, organizationID, memberID, profileID, provider, status string) {
		t.Helper()
		if _, err := store.pool.Exec(ctx, `INSERT INTO integrations_connections (
			id, organization_id, member_id, profile_id, provider, piece_name, piece_version,
			provider_subject_id, granted_scopes, encrypted_credentials, status, version, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,'piece','1','subject','{}','{}',$6,1,$7,$7)`, id, organizationID, memberID, profileID, provider, status, now); err != nil {
			t.Fatal(err)
		}
	}
	insertConnection(connections[0], organizations[0], "member", profiles[0], "microsoft", "active")
	insertConnection(connections[1], organizations[1], "member", profiles[1], "microsoft", "active")
	insertConnection(connections[2], organizations[0], "member-inactive", profiles[0], "microsoft", "reauthorization_required")
	insertConnection(connections[3], organizations[0], "member-slack", profiles[0], "slack", "active")
	insertTrigger := func(id, organizationID, connectionID, provider, triggerName, status string) {
		t.Helper()
		if _, err := store.pool.Exec(ctx, `INSERT INTO integrations_triggers (
			id, organization_id, member_id, connection_id, profile_id, provider, piece_name,
			piece_version, trigger_name, provider_identifier, props, status, version, created_at, updated_at
		) VALUES ($1,$2,'member',$3,'profile',$4,'piece','1',$5,'identifier','{}',$6,1,$7,$7)`, id, organizationID, connectionID, provider, triggerName, status, now); err != nil {
			t.Fatal(err)
		}
	}
	insertTrigger(triggers[0], organizations[0], connections[0], "microsoft", "new-email", "active")
	insertTrigger(triggers[1], organizations[0], connections[0], "microsoft", "calendar-event-changed", "active")
	insertTrigger(triggers[2], organizations[1], connections[1], "microsoft", "new-email", "active")
	insertTrigger(triggers[3], organizations[0], connections[0], "microsoft", "new-email", "disabled")
	insertTrigger(triggers[4], organizations[0], connections[3], "slack", "new-message", "active")
	insertTrigger(triggers[5], organizations[0], connections[2], "microsoft", "new-email", "active")
	insertTrigger(triggers[6], organizations[0], connections[0], "microsoft", "not-a-graph-trigger", "active")

	first, err := store.ActiveMicrosoftGraphTriggers(ctx, "", "", 2)
	if err != nil || !slices.Equal(triggerReferences(first), []string{organizations[0] + "/" + triggers[0], organizations[0] + "/" + triggers[1]}) {
		t.Fatalf("first page = %#v/%v", triggerReferences(first), err)
	}
	second, err := store.ActiveMicrosoftGraphTriggers(ctx, first[1].OrganizationID, first[1].ID, 2)
	if err != nil || !slices.Equal(triggerReferences(second), []string{organizations[1] + "/" + triggers[2]}) {
		t.Fatalf("second page = %#v/%v", triggerReferences(second), err)
	}
	// Profile disable keeps historical connection rows for cleanup, but must
	// immediately exclude their otherwise-active triggers from renewal.
	if _, err := store.pool.Exec(ctx, `UPDATE integrations_oauth_apps SET status = 'disabled' WHERE organization_id = $1 AND id = $2`, organizations[1], profiles[1]); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.ActiveMicrosoftGraphTriggers(ctx, "", "", 500)
	if err != nil || !slices.Equal(triggerReferences(remaining), []string{organizations[0] + "/" + triggers[0], organizations[0] + "/" + triggers[1]}) {
		t.Fatalf("disabled profile renewal references = %#v/%v", triggerReferences(remaining), err)
	}
	if _, err := store.ActiveMicrosoftGraphTriggers(ctx, "", "", 0); err == nil {
		t.Fatal("zero limit was accepted")
	}
}

func triggerReferences(triggers []domain.Trigger) []string {
	result := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		result = append(result, trigger.OrganizationID+"/"+trigger.ID)
	}
	return result
}
