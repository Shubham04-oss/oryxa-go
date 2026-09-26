package interactions

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/interactions -run TestStoreLifecycle
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-interactions-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_interactions_test_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID)
	}()

	now := time.Now().UTC().Truncate(time.Microsecond)
	request := agents.Interaction{
		ID: fmt.Sprintf("irq_test_%d", suffix), OrganizationID: organizationID,
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", UserEmail: "b@example.com"},
		Purpose:   "Architecture", Task: "Explain the boundary.",
		Context:        []agents.InteractionContextItem{{Label: "service", Value: "agents"}},
		RequestedTools: []string{"knowledge_search"}, RequestedPermissions: []string{"read:architecture"},
		Status: StatusPending, Revision: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.Create(ctx, request, "same-call")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Create(ctx, request, "same-call")
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	inbox, err := store.Inbox(ctx, organizationID, "user_b", 20)
	if err != nil || len(inbox) != 1 || inbox[0].Context[0].Value != "agents" {
		t.Fatalf("inbox=%+v err=%v", inbox, err)
	}

	approved, err := store.Decide(ctx, request.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved", AllowedTools: []string{"knowledge_search"}, GrantedPermissions: []string{"read:architecture"},
	}, agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}, now.Add(time.Minute))
	if err != nil || approved.Status != StatusApproved || approved.Revision != 2 {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	completed, err := store.Respond(ctx, request.ID, Unguarded(agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Answered", Content: "The boundary is EventBridge.", ToolsUsed: []string{"knowledge_search"},
	}), now.Add(2*time.Minute))
	if err != nil || completed.Status != StatusCompleted || completed.Result == nil || completed.Revision != 3 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}

	var events int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1`, request.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("events=%d, want 3", events)
	}
	var contentEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'request_id'=$1 AND (detail::text ILIKE '%Explain the boundary%' OR detail::text ILIKE '%The boundary is EventBridge%')`, request.ID).Scan(&contentEvents); err != nil {
		t.Fatal(err)
	}
	if contentEvents != 0 {
		t.Fatalf("%d routing events leaked request or response content", contentEvents)
	}
	if err := store.Forget(ctx, organizationID, "user_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, request.ID); err != ErrNotFound {
		t.Fatalf("forgotten request error=%v", err)
	}
}
