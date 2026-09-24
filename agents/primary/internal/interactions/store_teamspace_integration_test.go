package interactions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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

// TestTheLedgerListsTheRequestsBetweenTwoMembersInATeamspace: the store's own
// selection, newest first, for each way a listing is narrowed.
func TestTheLedgerListsTheRequestsBetweenTwoMembersInATeamspace(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	start := time.Now().UTC().Truncate(time.Microsecond)
	for index, seed := range []struct{ id, requester, target, teamspaceID string }{
		{"ab_room", "user_a", "user_b", "tsp_1"},
		{"ba_room", "user_b", "user_a", "tsp_1"},
		{"ab_outside", "user_a", "user_b", ""},
		{"ac_room", "user_a", "user_c", "tsp_1"},
		{"bc_room", "user_b", "user_c", "tsp_1"},
		{"ab_other_room", "user_a", "user_b", "tsp_2"},
	} {
		request := storedRequest(organizationID, organizationID+"_"+seed.id, seed.requester, seed.target, start.Add(time.Duration(index)*time.Second))
		request.TeamspaceID = seed.teamspaceID
		if _, err := store.Create(ctx, request, seed.id); err != nil {
			t.Fatal(err)
		}
	}
	// Other organizations' requests between the same members never appear.
	elsewhere := storedRequest(organizationID+"_elsewhere", organizationID+"_elsewhere", "user_a", "user_b", start)
	elsewhere.TeamspaceID = "tsp_1"
	if _, err := store.Create(ctx, elsewhere, "elsewhere"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID+"_elsewhere")
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID+"_elsewhere")
	})

	ids := func(userID string, query Query) string {
		t.Helper()
		listed, err := store.List(ctx, organizationID, userID, query)
		if err != nil {
			t.Fatalf("List(%s, %+v): %v", userID, query, err)
		}
		var names []string
		for _, request := range listed {
			names = append(names, strings.TrimPrefix(request.ID, organizationID+"_"))
		}
		return fmt.Sprint(names)
	}
	for _, testCase := range []struct {
		name, userID string
		query        Query
		want         string
	}{
		{"A with B in tsp_1", "user_a", Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_b"}, "[ba_room ab_room]"},
		{"B with A in tsp_1", "user_b", Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_a"}, "[ba_room ab_room]"},
		{"C with A in tsp_1", "user_c", Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_a"}, "[ac_room]"},
		{"A's inbox in tsp_1", "user_a", Query{Box: BoxInbox, TeamspaceID: "tsp_1"}, "[ba_room]"},
		{"A's sent requests in tsp_1", "user_a", Query{Box: BoxSent, TeamspaceID: "tsp_1"}, "[ac_room ab_room]"},
		{"A's sent requests to B", "user_a", Query{Box: BoxSent, WithUserID: "user_b"}, "[ab_other_room ab_outside ab_room]"},
		{"everything A is party to", "user_a", Query{Box: BoxAll}, "[ab_other_room ac_room ab_outside ba_room ab_room]"},
		{"the newest two", "user_a", Query{Box: BoxAll, Limit: 2}, "[ab_other_room ac_room]"},
	} {
		if got := ids(testCase.userID, testCase.query); got != testCase.want {
			t.Errorf("%s = %s, want %s", testCase.name, got, testCase.want)
		}
	}
	if inbox, err := store.Inbox(ctx, organizationID, "user_b", 20); err != nil || len(inbox) != 3 {
		t.Fatalf("B's inbox = %+v, %v; want the three A addressed to B", inbox, err)
	}
	if _, err := store.List(ctx, organizationID, "user_a", Query{Box: "everything"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown listing: error=%v, want ErrInvalid", err)
	}
}

// TestASideConversationsListingUsesTheTeamspaceIndexOnceItsPlanIsGeneric: a
// side conversation polls the same listing again and again, so the statement
// pgx caches soon runs on a generic plan. That plan must still reach one
// teamspace's requests through their index rather than read, and sort, every
// request in the organization.
func TestASideConversationsListingUsesTheTeamspaceIndexOnceItsPlanIsGeneric(t *testing.T) {
	database, _ := openInteractionStore(t)
	ctx := context.Background()
	statement, _, err := listStatement("org_1", "user_a", Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_b", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	err = pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		// With sequential scans off the plan shows what the indexes allow,
		// not what a nearly empty test table makes cheapest.
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		// GENERIC_PLAN plans the $n placeholders without values; pgconn sends
		// the text as it is, where pgx would want arguments for them.
		results, err := tx.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, COSTS OFF) `+statement).ReadAll()
		if err != nil {
			return err
		}
		for _, result := range results {
			for _, row := range result.Rows {
				plan = append(plan, string(row[0]))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if explained := strings.Join(plan, "\n"); !strings.Contains(explained, "agent_interactions_teamspace_idx") {
		t.Fatalf("the generic plan does not use the teamspace index:\n%s", explained)
	}
}
