package interactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
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
		WHERE table_schema='agents_primary' AND table_name='agent_interactions'
		  AND column_name IN ('teamspace_id', 'approval', 'decided_by_kind', 'decided_by_user_id', 'decided_by_agent_id')
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
	if columns["approval"] != "'agent'::text nullable=NO" || columns["teamspace_id"] != "''::text nullable=NO" ||
		columns["decided_by_kind"] != "''::text nullable=NO" || columns["decided_by_user_id"] != "''::text nullable=NO" ||
		columns["decided_by_agent_id"] != "''::text nullable=NO" {
		t.Fatalf("columns=%v", columns)
	}
	var constraints, indexes int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname IN ('agent_interactions_approval_check', 'agent_interactions_decided_by_check')
		  AND conrelid='agents_primary.agent_interactions'::regclass AND convalidated`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='agents_primary' AND indexname='agent_interactions_teamspace_idx'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if constraints != 2 || indexes != 1 {
		t.Fatalf("validated approval and decider checks=%d teamspace indexes=%d, want two and one", constraints, indexes)
	}
}

// earlier002 is the checksum of 002 as it was before it added its check NOT
// VALID and left the listing index to 004: what a database that ran it has
// recorded.
const earlier002 = "7704a7618a90f43719b8e05a9cdcc6493bc1991e1836d0acd8a014a31b8718e0"

// TestAnEarlierTeamspaceMigrationEndsUpValidated: two databases this release
// meets. One ran 002 before it said NOT VALID, so its check is already valid
// and 002 is recorded under that file's checksum; another ran this 002 but
// not yet 003, so its check is still NOT VALID. Migrating either ends with
// every file recorded as it is now, the check validated, and the listing
// index built.
func TestAnEarlierTeamspaceMigrationEndsUpValidated(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	for _, testCase := range []struct {
		name  string
		setUp string
	}{
		{"an earlier 002, its check already valid", `UPDATE oryxa_migrations SET checksum='` + earlier002 + `'
			WHERE service='agent-interactions' AND name='002_teamspace_approval.sql'`},
		// Dropped and added again in one transaction, so no other session
		// ever sees the table without it.
		{"this 002 without 003, its check not yet valid", `ALTER TABLE agents_primary.agent_interactions
			DROP CONSTRAINT agent_interactions_approval_check,
			ADD CONSTRAINT agent_interactions_approval_check CHECK (approval IN ('agent', 'owner')) NOT VALID`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, testCase.setUp); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `DELETE FROM oryxa_migrations WHERE service='agent-interactions' AND name > '002_teamspace_approval.sql'`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.Migrate(ctx); err != nil {
				t.Fatalf("migrating: %v", err)
			}
			var validated bool
			if err := database.Pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint
				WHERE conname='agent_interactions_approval_check' AND conrelid='agents_primary.agent_interactions'::regclass`).Scan(&validated); err != nil || !validated {
				t.Fatalf("the approval check is validated=%v (%v), want validated", validated, err)
			}
			names, err := fs.Glob(migrations, "migrations/*.sql")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				contents, err := fs.ReadFile(migrations, name)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(contents)
				var recorded string
				if err := database.Pool.QueryRow(ctx, `SELECT checksum FROM oryxa_migrations WHERE service='agent-interactions' AND name=$1`,
					path.Base(name)).Scan(&recorded); err != nil || recorded != hex.EncodeToString(sum[:]) {
					t.Fatalf("%s is recorded as %q (%v), want this file's checksum", name, recorded, err)
				}
			}
			var indexes int
			if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
				WHERE schemaname='agents_primary' AND indexname='agent_interactions_teamspace_idx'`).Scan(&indexes); err != nil || indexes != 1 {
				t.Fatalf("teamspace indexes=%d (%v), want one", indexes, err)
			}
		})
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
		if seed.teamspaceID != "" {
			request.Approval = ApprovalOwner
		}
		if _, err := store.Create(ctx, request, seed.id); err != nil {
			t.Fatal(err)
		}
	}
	// Other organizations' requests between the same members never appear.
	elsewhere := storedRequest(organizationID+"_elsewhere", organizationID+"_elsewhere", "user_a", "user_b", start)
	elsewhere.TeamspaceID, elsewhere.Approval = "tsp_1", ApprovalOwner
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

// planNode is one node of an EXPLAIN (FORMAT JSON) plan.
type planNode struct {
	NodeType  string     `json:"Node Type"`
	IndexName string     `json:"Index Name"`
	Filter    string     `json:"Filter"`
	Plans     []planNode `json:"Plans"`
}

// genericPlan is the plan PostgreSQL settles on for a statement once pgx has
// cached it and runs it again and again, as a side conversation's polling
// does: planned for its placeholders, not for any one set of values.
func genericPlan(t *testing.T, database *postgreskit.Database, statement string) planNode {
	t.Helper()
	ctx := context.Background()
	var explained []byte
	err := pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		// With sequential scans off the plan shows what the indexes allow,
		// not what a nearly empty test table makes cheapest.
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		// GENERIC_PLAN plans the $n placeholders without values; pgconn sends
		// the text as it is, where pgx would want arguments for them.
		results, err := tx.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, COSTS OFF, FORMAT JSON) `+statement).ReadAll()
		if err != nil {
			return err
		}
		explained = results[0].Rows[0][0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(explained, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode the plan: %v\n%s", err, explained)
	}
	return plans[0].Plan
}

func (node planNode) walk(visit func(planNode)) {
	visit(node)
	for _, child := range node.Plans {
		child.walk(visit)
	}
}

// TestAListingReadsEachSideInOrderFromItsOwnIndex: a side conversation polls
// the requests between two teammates in a teamspace, and a member's list
// reads everything they are party to. On the generic plan each direction is
// read, in order, from an index that leads with its own columns and stops at
// the limit: no sort, no sequential scan, and no filter left over on who
// asked whom — where an OR across both columns read every request in the
// teamspace, or in the organization, and filtered or sorted them.
func TestAListingReadsEachSideInOrderFromItsOwnIndex(t *testing.T) {
	database, _ := openInteractionStore(t)
	for _, testCase := range []struct {
		name    string
		query   Query
		indexes []string
	}{
		{"a side conversation's poll", Query{Box: BoxAll, TeamspaceID: "tsp_1", WithUserID: "user_b", Limit: 20},
			[]string{"agent_interactions_teamspace_pair_idx", "agent_interactions_teamspace_pair_idx"}},
		{"everything a member is party to", Query{Box: BoxAll, Limit: 20},
			[]string{"agent_interactions_inbox_idx", "agent_interactions_sent_idx"}},
		{"a member's inbox", Query{Box: BoxInbox, Limit: 20}, []string{"agent_interactions_inbox_idx"}},
		{"a member's sent requests", Query{Box: BoxSent, Limit: 20}, []string{"agent_interactions_sent_idx"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			statement, _, err := listStatement("org_1", "user_a", testCase.query)
			if err != nil {
				t.Fatal(err)
			}
			plan := genericPlan(t, database, statement)
			var scans []string
			plan.walk(func(node planNode) {
				switch {
				case strings.Contains(node.NodeType, "Sort") || node.NodeType == "Seq Scan" || strings.HasPrefix(node.NodeType, "Bitmap"):
					t.Errorf("the plan has a %s node", node.NodeType)
				case strings.Contains(node.Filter, "target_user_id") || strings.Contains(node.Filter, "requester_user_id"):
					t.Errorf("a %s filters on who asked whom: %s", node.NodeType, node.Filter)
				}
				if node.IndexName != "" {
					scans = append(scans, node.IndexName)
				}
			})
			slices.Sort(scans)
			if !slices.Equal(scans, testCase.indexes) {
				t.Errorf("the plan reads %v, want %v", scans, testCase.indexes)
			}
		})
	}
}

// TestTheLedgerFindsARequestByItsKey: the key names one request of one
// requesting agent in one organization, and nothing else.
func TestTheLedgerFindsARequestByItsKey(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := storedRequest(organizationID, organizationID+"_keyed", "user_a", "user_b", now)
	request.TeamspaceID, request.Approval = "tsp_1", ApprovalOwner
	if _, err := store.Create(ctx, request, "side-sdc-1"); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindByKey(ctx, organizationID, "agt_user_a", "side-sdc-1")
	if err != nil || found.ID != request.ID || found.TeamspaceID != "tsp_1" || found.Approval != ApprovalOwner {
		t.Fatalf("FindByKey = %+v, %v; want %s", found, err, request.ID)
	}
	for name, lookup := range map[string][3]string{
		"another key":          {organizationID, "agt_user_a", "side-sdc-2"},
		"another agent":        {organizationID, "agt_user_b", "side-sdc-1"},
		"another organization": {organizationID + "_other", "agt_user_a", "side-sdc-1"},
	} {
		if found, err := store.FindByKey(ctx, lookup[0], lookup[1], lookup[2]); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: FindByKey = %+v, %v; want ErrNotFound", name, found, err)
		}
	}
}

// TestTheLedgerKeepsWhoDecided: the decider is stored in columns of its own
// and read back into the decision; a decision an older build recorded reads
// with no decider; and the database refuses a decider that does not add up.
func TestTheLedgerKeepsWhoDecided(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	approve := agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go ahead."}
	for _, decider := range []agents.InteractionDecider{
		{Kind: DecidedByMember, UserID: "user_b"},
		{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"},
		{Kind: DecidedBySystem},
	} {
		request := storedRequest(organizationID, organizationID+"_"+decider.Kind, "user_a", "user_b", now)
		if _, err := store.Create(ctx, request, decider.Kind); err != nil {
			t.Fatal(err)
		}
		decided, err := store.Decide(ctx, request.ID, approve, decider, now.Add(time.Second))
		if err != nil || decided.Decision == nil || decided.Decision.DecidedBy == nil || *decided.Decision.DecidedBy != decider {
			t.Fatalf("decided by %+v = %+v, %v", decider, decided.Decision, err)
		}
		read, err := store.Get(ctx, request.ID)
		if err != nil || read.Decision.DecidedBy == nil || *read.Decision.DecidedBy != decider {
			t.Fatalf("read back = %+v, %v; want decided by %+v", read.Decision, err, decider)
		}
	}

	older := storedRequest(organizationID, organizationID+"_older", "user_a", "user_b", now)
	if _, err := store.Create(ctx, older, "older"); err != nil {
		t.Fatal(err)
	}
	// An older build's Decide writes the decision and nothing else.
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET status='approved',
		decision='{"decision":"approved","message":"Approved.","allowed_tools":[],"granted_permissions":[],"decided_at":"2026-09-25T00:00:00Z","decided_by":{"kind":"member","user_id":"user_b"}}'
		WHERE id=$1`, older.ID); err != nil {
		t.Fatal(err)
	}
	if read, err := store.Get(ctx, older.ID); err != nil || read.Decision == nil || read.Decision.DecidedBy != nil {
		t.Fatalf("an older build's decision reads %+v, %v; want no decider, whatever its JSON claims", read.Decision, err)
	}

	for name, columns := range map[string][3]string{
		"a member with an agent":   {"member", "user_b", "agt_b"},
		"an agent with no member":  {"agent", "", "agt_b"},
		"an agent with no agent":   {"agent", "user_b", ""},
		"the system with a member": {"system", "user_b", ""},
		"a kind nobody knows":      {"team", "user_b", ""},
		"no kind, with a member":   {"", "user_b", ""},
	} {
		if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions
			SET decided_by_kind=$2, decided_by_user_id=$3, decided_by_agent_id=$4 WHERE id=$1`,
			older.ID, columns[0], columns[1], columns[2]); err == nil {
			t.Errorf("the ledger stored %s as its decider", name)
		}
	}
}

var (
	requestedEvent = agentsv1.InteractionRequestedDetailType + ":pending"
	approvedEvent  = agentsv1.InteractionDecidedDetailType + ":approved"
)

// eventsOf is the detail types the outbox holds for one request, oldest
// first, with the status each carried.
func eventsOf(t *testing.T, database *postgreskit.Database, requestID string) []string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `SELECT detail_type || ':' || (detail->>'status')
		FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1 ORDER BY occurred_at, detail_type`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

// TestTheLedgerHoldsARequestForItsOwner: a pending request is marked held,
// and an approved one has its approval and its decider withdrawn and is
// pending again, held, each recorded as a new requested event that says so;
// holding one again changes nothing; anything else is not the ledger's to
// hold.
func TestTheLedgerHoldsARequestForItsOwner(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	create := func(name string) agents.Interaction {
		t.Helper()
		request := storedRequest(organizationID, organizationID+"_"+name, "user_a", "user_b", now)
		request.TeamspaceID, request.Approval = "tsp_1", ApprovalAgent
		if _, err := store.Create(ctx, request, name); err != nil {
			t.Fatal(err)
		}
		return request
	}

	pending := create("pending")
	held, err := store.Hold(ctx, pending.ID, now.Add(time.Second))
	if err != nil || held.Status != StatusPending || held.Approval != ApprovalOwner || held.Revision != 2 {
		t.Fatalf("holding a pending request = %+v, %v", held, err)
	}
	if again, err := store.Hold(ctx, pending.ID, now.Add(2*time.Second)); err != nil || again.Revision != 2 {
		t.Fatalf("holding it again = %+v, %v; want it unchanged", again, err)
	}
	if events := eventsOf(t, database, pending.ID); fmt.Sprint(events) != fmt.Sprint([]string{requestedEvent, requestedEvent}) {
		t.Fatalf("events=%v, want the request's own and a new requested event for the hold, and none for holding it again", events)
	}
	if _, changes := changesOf(t, database, pending.ID); len(changes) != 2 || changes[1].Approval != ApprovalOwner ||
		changes[1].Revision != 2 || !changes[1].ChangedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("the hold's event = %+v, want it held for its owner at revision 2, when it was held", changes)
	}

	approved := create("approved")
	if _, err := store.Decide(ctx, approved.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go."},
		agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rehold, err := store.Hold(ctx, approved.ID, now.Add(2*time.Second))
	if err != nil || rehold.Status != StatusPending || rehold.Approval != ApprovalOwner || rehold.Decision != nil || rehold.Revision != 3 {
		t.Fatalf("holding an approved request = %+v, %v; want it pending, held, its approval withdrawn", rehold, err)
	}
	var decider string
	if err := database.Pool.QueryRow(ctx, `SELECT decided_by_kind || decided_by_user_id || decided_by_agent_id
		FROM agents_primary.agent_interactions WHERE id=$1`, approved.ID).Scan(&decider); err != nil || decider != "" {
		t.Fatalf("its decider is %q (%v), want it withdrawn too", decider, err)
	}
	if events := eventsOf(t, database, approved.ID); fmt.Sprint(events) != fmt.Sprint([]string{requestedEvent, approvedEvent, requestedEvent}) {
		t.Fatalf("events=%v, want a new requested event after the withdrawn approval", events)
	}

	finished := create("finished")
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET status='completed' WHERE id=$1`, finished.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Hold(ctx, finished.ID, now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("holding a completed request: error=%v, want ErrConflict", err)
	}
	if _, err := store.Hold(ctx, create("expired").ID, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("holding an expired request: error=%v, want ErrExpired", err)
	}
}

// TestTheLedgerClosesARequestThatNoLongerStands: Oryxa rejects a pending
// request, as its decider, and fails an approved one before its work, each
// with the reason and the event that tells the member who asked.
func TestTheLedgerClosesARequestThatNoLongerStands(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	reason := "Oryxa closed this request because the member who asked is no longer on the teamspace's team."
	create := func(name string) agents.Interaction {
		t.Helper()
		request := storedRequest(organizationID, organizationID+"_"+name, "user_a", "user_b", now)
		request.TeamspaceID, request.Approval = "tsp_1", ApprovalOwner
		if _, err := store.Create(ctx, request, name); err != nil {
			t.Fatal(err)
		}
		return request
	}

	pending := create("pending")
	rejected, err := store.Close(ctx, pending.ID, reason, now.Add(time.Second))
	if err != nil || rejected.Status != StatusRejected || rejected.Decision == nil || rejected.Decision.Message != reason ||
		rejected.Decision.DecidedBy == nil || *rejected.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedBySystem}) {
		t.Fatalf("closing a pending request = %+v, %v; want it rejected by Oryxa", rejected, err)
	}
	if events := eventsOf(t, database, pending.ID); fmt.Sprint(events) != fmt.Sprint([]string{requestedEvent, agentsv1.InteractionDecidedDetailType + ":rejected"}) {
		t.Fatalf("events=%v, want the rejection's", events)
	}

	approved := create("approved")
	if _, err := store.Decide(ctx, approved.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go."},
		agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Close(ctx, approved.ID, reason, now.Add(2*time.Second))
	if err != nil || failed.Status != StatusFailed || failed.Result == nil || failed.Result.Status != StatusFailed || failed.Result.Summary != reason ||
		failed.Decision == nil || failed.Decision.DecidedBy == nil || failed.Decision.DecidedBy.Kind != DecidedByMember {
		t.Fatalf("closing an approved request = %+v, %v; want it failed, its member's approval kept", failed, err)
	}
	if events := eventsOf(t, database, approved.ID); fmt.Sprint(events) != fmt.Sprint([]string{requestedEvent, approvedEvent, agentsv1.InteractionCompletedDetailType + ":failed"}) {
		t.Fatalf("events=%v, want the failed result's", events)
	}
	if _, err := store.Close(ctx, approved.ID, reason, now.Add(3*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("closing it twice: error=%v, want ErrConflict", err)
	}
	if _, err := store.Close(ctx, create("expired").ID, reason, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("closing an expired request: error=%v, want ErrExpired", err)
	}
}

// changesOf is what the outbox tells the bus about one request, oldest first:
// each event's detail type and its data.
func changesOf(t *testing.T, database *postgreskit.Database, requestID string) ([]string, []agentsv1.InteractionChanged) {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `SELECT detail_type, detail
		FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1 ORDER BY occurred_at, detail_type`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var detailTypes []string
	var changes []agentsv1.InteractionChanged
	for rows.Next() {
		var detailType string
		var detail []byte
		if err := rows.Scan(&detailType, &detail); err != nil {
			t.Fatal(err)
		}
		var change agentsv1.InteractionChanged
		if err := json.Unmarshal(detail, &change); err != nil {
			t.Fatal(err)
		}
		detailTypes = append(detailTypes, detailType)
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return detailTypes, changes
}

// TestARequestsEventsCarryItsTeamspaceApprovalAndDecider: every event the
// ledger records says where the request was made and who decides it, and,
// once it is decided, who did, exactly as the ledger stores them; a request
// outside a teamspace names none, and an approval withdrawn takes its decider
// with it.
func TestARequestsEventsCarryItsTeamspaceApprovalAndDecider(t *testing.T) {
	database, store := openInteractionStore(t)
	ctx := context.Background()
	organizationID := testOrganization(t, database)
	now := time.Now().UTC().Truncate(time.Microsecond)
	member := agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}
	agent := agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}
	decider := func(decider agents.InteractionDecider) *agentsv1.InteractionDecider {
		return &agentsv1.InteractionDecider{Kind: decider.Kind, UserID: decider.UserID, AgentID: decider.AgentID}
	}
	type change struct {
		detailType, status, teamspaceID, approval string
		decidedBy                                 *agentsv1.InteractionDecider
	}
	check := func(name, requestID string, want []change) {
		t.Helper()
		detailTypes, changes := changesOf(t, database, requestID)
		if len(changes) != len(want) {
			t.Fatalf("%s: events = %v, want %d", name, detailTypes, len(want))
		}
		for index, got := range changes {
			expected := want[index]
			if detailTypes[index] != expected.detailType || got.Status != expected.status || got.RequestID != requestID ||
				got.TeamspaceID != expected.teamspaceID || got.Approval != expected.approval ||
				(got.DecidedBy == nil) != (expected.decidedBy == nil) || (got.DecidedBy != nil && *got.DecidedBy != *expected.decidedBy) {
				t.Errorf("%s: event %d = %s %+v (decided by %+v), want %+v (decided by %+v)",
					name, index, detailTypes[index], got, got.DecidedBy, expected, expected.decidedBy)
			}
		}
	}

	// Held for its owner in a teamspace, approved by them, and answered.
	held := storedRequest(organizationID, organizationID+"_held", "user_a", "user_b", now)
	held.TeamspaceID, held.Approval = "tsp_1", ApprovalOwner
	if _, err := store.Create(ctx, held, "held"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, held.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go."},
		member, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Respond(ctx, held.ID, Unguarded(agents.InteractionResponseInput{Status: StatusCompleted, Summary: "Done", Content: "Reviewed."}),
		now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	check("held for its owner", held.ID, []change{
		{agentsv1.InteractionRequestedDetailType, StatusPending, "tsp_1", ApprovalOwner, nil},
		{agentsv1.InteractionDecidedDetailType, StatusApproved, "tsp_1", ApprovalOwner, decider(member)},
		{agentsv1.InteractionCompletedDetailType, StatusCompleted, "tsp_1", ApprovalOwner, decider(member)},
	})

	// Outside a teamspace, its agent decides, and rejects it.
	outside := storedRequest(organizationID, organizationID+"_outside", "user_a", "user_b", now)
	if _, err := store.Create(ctx, outside, "outside"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, outside.ID, agents.InteractionDecisionInput{Decision: DecisionRejected, Message: "No."},
		agent, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	check("outside a teamspace", outside.ID, []change{
		{agentsv1.InteractionRequestedDetailType, StatusPending, "", ApprovalAgent, nil},
		{agentsv1.InteractionDecidedDetailType, StatusRejected, "", ApprovalAgent, decider(agent)},
	})

	// An agent's approval withdrawn, and the request then closed by Oryxa.
	withdrawn := storedRequest(organizationID, organizationID+"_withdrawn", "user_a", "user_b", now)
	withdrawn.TeamspaceID, withdrawn.Approval = "tsp_1", ApprovalAgent
	if _, err := store.Create(ctx, withdrawn, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, withdrawn.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go."},
		agent, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Hold(ctx, withdrawn.ID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Close(ctx, withdrawn.ID, "The teamspace is gone.", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	check("withdrawn, then closed", withdrawn.ID, []change{
		{agentsv1.InteractionRequestedDetailType, StatusPending, "tsp_1", ApprovalAgent, nil},
		{agentsv1.InteractionDecidedDetailType, StatusApproved, "tsp_1", ApprovalAgent, decider(agent)},
		{agentsv1.InteractionRequestedDetailType, StatusPending, "tsp_1", ApprovalOwner, nil},
		{agentsv1.InteractionDecidedDetailType, StatusRejected, "tsp_1", ApprovalOwner, &agentsv1.InteractionDecider{Kind: DecidedBySystem}},
	})
}
