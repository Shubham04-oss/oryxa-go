package interactions

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestTheLedgerKeepsWhoChoseARequestsTools: what a request asks for and who
// chose it are stored and read back; a row an older build wrote reads as
// chosen by nobody on record, which the API leaves out; and the ledger holds
// no fourth chooser.
func TestTheLedgerKeepsWhoChoseARequestsTools(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	at := time.Now().UTC().Truncate(time.Microsecond)

	judged := storedRequest(organizationID, "irq_judged_"+organizationID, "user_a", "user_b", at)
	judged.TeamspaceID, judged.Approval = "tsp_1", ApprovalOwner
	judged.RequestedTools, judged.ToolsJudgedBy = []string{agents.AppsToolboxName, agents.KnowledgeToolboxName}, ToolsJudgedByJev
	if _, err := store.Create(ctx, judged, "judged"); err != nil {
		t.Fatal(err)
	}
	read, err := store.Get(ctx, judged.ID)
	if err != nil || read.ToolsJudgedBy != ToolsJudgedByJev || !slices.Equal(read.RequestedTools, judged.RequestedTools) {
		t.Fatalf("read=%+v err=%v, want apps and knowledge chosen by Jev", read, err)
	}

	older := storedRequest(organizationID, "irq_older_"+organizationID, "user_a", "user_b", at)
	if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.agent_interactions
		(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name,
		 target_user_id, target_agent_id, target_agent_name, purpose, task, status, idempotency_key, expires_at)
		VALUES ($1,$2,'user_a','agt_user_a','Crew','user_b','agt_user_b','Carl','Launch','Review.','pending','older',$3)`,
		older.ID, organizationID, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	read, err = store.Get(ctx, older.ID)
	if err != nil || read.ToolsJudgedBy != "" {
		t.Fatalf("an older build's request = %+v, %v; want nobody on record", read, err)
	}
	encoded, err := json.Marshal(read)
	if err != nil || strings.Contains(string(encoded), "tools_judged_by") {
		t.Fatalf("an older build's request reads %s (%v), want no tools_judged_by", encoded, err)
	}

	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET tools_judged_by='someone' WHERE id=$1`, judged.ID); err == nil {
		t.Fatal("the ledger stored a chooser it does not know")
	}
}
