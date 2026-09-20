package interactiondelivery

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-agent-interaction-delivery-test", Schema: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := interactions.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	organizationID := fmt.Sprintf("org_delivery_test_%d", suffix)
	requestID := fmt.Sprintf("irq_delivery_test_%d", suffix)
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_deliveries WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_delivery_threads WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID)
	}()

	now := time.Now().UTC().Truncate(time.Microsecond)
	requested := Delivery{
		EventID: fmt.Sprintf("evt_requested_%d", suffix), RequestID: requestID, OrganizationID: organizationID,
		RecipientUserID: "user_b", RecipientAgentID: "agt_b", Kind: KindTargetRequested,
		Status: "pending", Revision: 1, ChangedAt: now,
	}
	approved := requested
	approved.EventID, approved.Kind, approved.Status, approved.Revision = fmt.Sprintf("evt_approved_%d", suffix), KindTargetApproved, "approved", 2

	// Record the later revision first: Claim still delivers the lifecycle in
	// revision order once the earlier event arrives.
	if added, err := store.Record(ctx, approved); err != nil || !added {
		t.Fatalf("record approved: added=%v err=%v", added, err)
	}
	if added, err := store.Record(ctx, requested); err != nil || !added {
		t.Fatalf("record requested: added=%v err=%v", added, err)
	}
	if added, err := store.Record(ctx, requested); err != nil || added {
		t.Fatalf("repeat requested: added=%v err=%v", added, err)
	}

	claimed, err := store.Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].EventID != requested.EventID {
		t.Fatalf("first claim=%+v err=%v", claimed, err)
	}
	if err := store.Done(ctx, requested.EventID); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Claim(ctx, time.Minute, 10)
	if err != nil || len(claimed) != 1 || claimed[0].EventID != approved.EventID {
		t.Fatalf("second claim=%+v err=%v", claimed, err)
	}

	if err := store.SetThread(ctx, approved, "thread_1"); err != nil {
		t.Fatal(err)
	}
	if threadID, err := store.Thread(ctx, requestID, "agt_b"); err != nil || threadID != "thread_1" {
		t.Fatalf("thread=%q err=%v", threadID, err)
	}
	if err := store.ClearThread(ctx, requestID, "agt_b", "another_thread"); err != nil {
		t.Fatal(err)
	}
	if threadID, _ := store.Thread(ctx, requestID, "agt_b"); threadID != "thread_1" {
		t.Fatalf("wrong expected thread cleared %q", threadID)
	}
	if err := store.ClearThread(ctx, requestID, "agt_b", "thread_1"); err != nil {
		t.Fatal(err)
	}
	if threadID, _ := store.Thread(ctx, requestID, "agt_b"); threadID != "" {
		t.Fatalf("thread not cleared %q", threadID)
	}

	if dropped, err := store.Release(ctx, approved.EventID, true, 1, 0, "model failed"); err != nil || !dropped {
		t.Fatalf("release: dropped=%v err=%v", dropped, err)
	}

	// Both parties' delivery conversations are discoverable for cleanup when
	// either party leaves, not only the departed member's own conversation.
	_, err = database.Pool.Exec(ctx, `INSERT INTO agents_primary.agent_interactions
		(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name,
		 target_user_id, target_agent_id, target_agent_name, purpose, task, status,
		 idempotency_key, revision, expires_at, created_at, updated_at)
		VALUES ($1,$2,'user_a','agt_a','Requester','user_b','agt_b','Target',
			'test','test','completed','test',3,$3,$4,$4)`, requestID, organizationID, now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetThread(ctx, approved, "thread_b"); err != nil {
		t.Fatal(err)
	}
	requester := approved
	requester.RecipientUserID, requester.RecipientAgentID = "user_a", "agt_a"
	if err := store.SetThread(ctx, requester, "thread_a"); err != nil {
		t.Fatal(err)
	}
	affected, err := store.AffectedThreads(ctx, organizationID, "user_a")
	if err != nil || len(affected) != 2 {
		t.Fatalf("affected=%+v err=%v", affected, err)
	}
	if err := store.Forget(ctx, organizationID, "user_a"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.interaction_deliveries WHERE organization_id=$1`, organizationID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}
