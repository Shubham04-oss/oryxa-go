package interactions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

func TestRequesterThreadMigrationBackfillsLegacyRowsAndCanRepeat(t *testing.T) {
	database, _ := openInteractionStore(t)
	ctx := context.Background()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE requester_thread_upgrade (id text PRIMARY KEY) ON COMMIT DROP;
		INSERT INTO requester_thread_upgrade (id) VALUES ('legacy')`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrations.ReadFile("migrations/016_requester_thread.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual additive migration against a pre-upgrade table,
	// without removing a column used by the shared local agents service.
	statement := strings.ReplaceAll(string(migration), "agents_primary.agent_interactions", "pg_temp.requester_thread_upgrade")
	for range 2 {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO requester_thread_upgrade (id) VALUES ('old-build');
		INSERT INTO requester_thread_upgrade (id, requester_thread_id, requester_thread_app_name) VALUES ('new-build', 'thread_origin', 'krude')`); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"legacy": "", "old-build": "", "new-build": "thread_origin"} {
		var got string
		if err := tx.QueryRow(ctx, `SELECT requester_thread_id FROM requester_thread_upgrade WHERE id=$1`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: requester_thread_id=%q err=%v", id, got, err)
		}
		var application string
		if err := tx.QueryRow(ctx, `SELECT requester_thread_app_name FROM requester_thread_upgrade WHERE id=$1`, id).Scan(&application); err != nil {
			t.Fatal(err)
		}
		if want == "" && application != "" || want != "" && application != "krude" {
			t.Fatalf("%s: requester_thread_app_name=%q", id, application)
		}
	}
}

func TestStoredRequestRetainsItsOriginAcrossReadsAndTransitions(t *testing.T) {
	database, store := openInteractionStore(t)
	organizationID := testOrganization(t, database)
	ctx := context.Background()
	now := time.Now().UTC()
	request := storedRequest(organizationID, "irq_"+organizationID, "asker", "owner", now)
	request.RequesterThreadID = "thread_origin"
	request.RequesterThreadAppName = agents.RootName
	created, err := store.Create(ctx, request, "origin-call")
	if err != nil || created.RequesterThreadID != "thread_origin" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	read, err := store.Get(ctx, created.ID)
	if err != nil || read.RequesterThreadID != "thread_origin" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	replayed, err := store.FindByKey(ctx, organizationID, request.Requester.AgentID, "origin-call")
	if err != nil || replayed.RequesterThreadID != "thread_origin" {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	sent, err := store.Sent(ctx, organizationID, "asker", 20)
	if err != nil || len(sent) != 1 || sent[0].RequesterThreadID != "thread_origin" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	approved, err := store.Decide(ctx, created.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved",
	}, agents.InteractionDecider{Kind: DecidedByMember, UserID: "owner"}, now.Add(time.Second))
	if err != nil || approved.RequesterThreadID != "thread_origin" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	completed, err := store.Respond(ctx, created.ID, Unguarded(agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "14 October",
	}), now.Add(2*time.Second))
	if err != nil || completed.RequesterThreadID != "thread_origin" || completed.RequesterThreadAppName != agents.RootName {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}
