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

// openInteractionStore migrates the request ledger into the database the
// integration tests share:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/interactions
func openInteractionStore(t *testing.T) (*postgreskit.Database, *Store) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-interactions-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return database, store
}

// testOrganization is an organization id no other run shares, whose requests
// and events are removed when the test ends.
func testOrganization(t *testing.T, database *postgreskit.Database) string {
	t.Helper()
	organizationID := fmt.Sprintf("org_teamspace_requests_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID)
	})
	return organizationID
}

func storedRequest(organizationID, id, requesterUserID, targetUserID string, at time.Time) agents.Interaction {
	return agents.Interaction{
		ID: id, OrganizationID: organizationID,
		Requester: agents.InteractionParty{UserID: requesterUserID, AgentID: "agt_" + requesterUserID, AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: targetUserID, AgentID: "agt_" + targetUserID, AgentName: "Carl"},
		Purpose:   "Launch", Task: "Review the launch plan.",
		Context: []agents.InteractionContextItem{}, RequestedTools: []string{}, RequestedPermissions: []string{},
		Status: StatusPending, Revision: 1, ExpiresAt: at.Add(time.Hour), CreatedAt: at, UpdatedAt: at,
	}
}

// TestTheTeamspaceMigrationIsSafeToApplyAgain: the runner applies a file
// again whenever its checksum changes. Make it do so, twice, and the schema
// ends up exactly as one application leaves it.
func TestTheTeamspaceMigrationIsSafeToApplyAgain(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	for attempt := range 2 {
		if _, err := database.Pool.Exec(ctx, `UPDATE oryxa_migrations SET checksum='stale'
			WHERE service='agent-interactions'`); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("applying the migrations again (attempt %d): %v", attempt+1, err)
		}
		var stale int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM oryxa_migrations
			WHERE service='agent-interactions' AND checksum='stale'`).Scan(&stale); err != nil {
			t.Fatal(err)
		}
		if stale != 0 {
			t.Fatalf("attempt %d left %d migrations unapplied", attempt+1, stale)
		}
	}

	rows, err := database.Pool.Query(ctx, `SELECT column_name, column_default, is_nullable
		FROM information_schema.columns
		WHERE table_schema='agents_primary' AND table_name='agent_interactions' AND column_name IN ('teamspace_id', 'approval')
		ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string]string{}
	for rows.Next() {
		var name, fallback, nullable string
		if err := rows.Scan(&name, &fallback, &nullable); err != nil {
			t.Fatal(err)
		}
		columns[name] = fallback + " nullable=" + nullable
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if columns["approval"] != "'agent'::text nullable=NO" || columns["teamspace_id"] != "''::text nullable=NO" {
		t.Fatalf("columns=%v", columns)
	}
	var constraints, indexes int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname='agent_interactions_approval_check' AND conrelid='agents_primary.agent_interactions'::regclass`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='agents_primary' AND indexname='agent_interactions_teamspace_idx'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if constraints != 1 || indexes != 1 {
		t.Fatalf("approval checks=%d teamspace indexes=%d, want one each", constraints, indexes)
	}
}

// TestARequestKeepsItsTeamspaceAndApproval: both are stored and read back; a
// row written before either existed reads as a request outside any
// teamspace that its agent decides; and the ledger holds no third approval.
func TestARequestKeepsItsTeamspaceAndApproval(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)

	held := storedRequest(organizationID, organizationID+"_held", "user_a", "user_b", now)
	held.TeamspaceID, held.Approval = "tsp_1", ApprovalOwner
	created, err := store.Create(ctx, held, "held")
	if err != nil || created.TeamspaceID != "tsp_1" || created.Approval != ApprovalOwner {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	read, err := store.Get(ctx, held.ID)
	if err != nil || read.TeamspaceID != "tsp_1" || read.Approval != ApprovalOwner {
		t.Fatalf("read=%+v err=%v", read, err)
	}

	unnamed := storedRequest(organizationID, organizationID+"_unnamed", "user_a", "user_b", now)
	if created, err := store.Create(ctx, unnamed, "unnamed"); err != nil || created.Approval != ApprovalAgent || created.TeamspaceID != "" {
		t.Fatalf("a request naming no approval = %+v, %v; want one its agent decides", created, err)
	}

	if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.agent_interactions
		(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name,
		 target_user_id, target_agent_id, target_agent_name, purpose, task, status, idempotency_key, expires_at)
		VALUES ($1,$2,'user_a','agt_a','Crew','user_b','agt_b','Carl','Old','Written before teamspaces.','pending','old',$3)`,
		organizationID+"_old", organizationID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	old, err := store.Get(ctx, organizationID+"_old")
	if err != nil || old.Approval != ApprovalAgent || old.TeamspaceID != "" {
		t.Fatalf("an existing request reads %+v, %v; want one outside any teamspace that its agent decides", old, err)
	}

	unknown := storedRequest(organizationID, organizationID+"_unknown", "user_a", "user_b", now)
	unknown.Approval = "anyone"
	if _, err := store.Create(ctx, unknown, "unknown"); err == nil {
		t.Fatal("the ledger stored an approval that is neither agent nor owner")
	}
}
