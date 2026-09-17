package adksession

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// TestPostgresSessionWritesOutboxEvents needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./internal/adksession
func TestPostgresSessionWritesOutboxEvents(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	outbox := events.NewOutbox(store.Database())
	if err := outbox.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	service := store.Sessions(events.NewSessionRecorder(outbox))

	created, err := service.Create(ctx, &session.CreateRequest{
		AppName: "krude-events-test", UserID: t.Name(),
		State: map[string]any{StateOrganizationID: "org_events_test", StateUserName: "Asha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := created.Session.ID()
	defer func() {
		if err := service.Delete(ctx, &session.DeleteRequest{AppName: "krude-events-test", UserID: t.Name(), SessionID: sessionID}); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
		_, _ = store.Database().SQLX.ExecContext(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'session_id' = $1`, sessionID)
	}()

	// Read the event back without claiming anything: this table is shared,
	// and a claim would hold other rows from a running dispatcher.
	var detail json.RawMessage
	if err := store.Database().SQLX.QueryRowContext(ctx, `
		SELECT detail FROM agents_primary.agents_outbox
		WHERE detail_type = $1 AND detail->>'session_id' = $2`,
		agentsv1.SessionCreatedDetailType, sessionID).Scan(&detail); err != nil {
		t.Fatalf("no %s event for session %s in the outbox: %v", agentsv1.SessionCreatedDetailType, sessionID, err)
	}
	var payload agentsv1.SessionCreated
	if err := json.Unmarshal(detail, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != t.Name() || payload.OrganizationID != "org_events_test" || payload.AppName != "krude-events-test" {
		t.Fatalf("payload = %+v", payload)
	}
}
