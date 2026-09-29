package interactions

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The members of the Slack asks in these tests: who asks, and the two owners
// they name.
const (
	asker = "user_asker"
	raya  = "user_raya"
	maya  = "user_maya"
)

// testClock is a clock a test moves: the ledger reads every time it compares
// from the clock it is given, never from the database's, so a lease's hour
// and a request's day pass when the test says.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now().UTC().Truncate(time.Microsecond)} }

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Advance(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(by)
}

// primaryAgents finds each member's primary agent as agt_<member>, and names
// it for them, as the registry does for a member it has made one for.
type primaryAgents struct{}

func (primaryAgents) Primary(_ context.Context, scope managedagents.Scope) (managedagents.Definition, error) {
	return managedagents.Definition{
		ID: "agt_" + scope.UserID, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID,
		Kind: managedagents.KindPrimary, Name: "Carl of " + scope.UserID, Status: managedagents.StatusActive,
	}, nil
}

// slackLedger is a ledger over the real store, for one organization no other
// test shares, with a clock the test moves and the tickets of a known key.
type slackLedger struct {
	t        *testing.T
	database *postgreskit.Database
	store    *Store
	service  *Service
	clock    *testClock
	org      string
	guard    *fakeGuard
}

// newSlackLedger opens the ledger. changes edit its configuration before it is
// built; without any, it has no guard, policy or workspace policy.
func newSlackLedger(t *testing.T, changes ...func(*Config)) *slackLedger {
	t.Helper()
	database, store := openInteractionStore(t)
	org := fmt.Sprintf("org_slack_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, org)
		for _, table := range []string{"interaction_leases", "interaction_asks", "agent_interactions"} {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.`+table+` WHERE organization_id=$1`, org)
		}
	})
	clock := newTestClock()
	config := Config{
		Repository: store, Directory: fakeDirectory{}, Now: clock.Now,
		Agents: primaryAgents{}, TicketKey: ticketKey,
	}
	for _, change := range changes {
		change(&config)
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := config.Guard.(*fakeGuard)
	return &slackLedger{t: t, database: database, store: store, service: service, clock: clock, org: org, guard: guard}
}

// inThread is where the asks of these tests are made unless a test says
// otherwise: a thread in one channel of one workspace.
func inThread(trigger string) SlackThread {
	return SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "1790000000.000100", TriggerTS: trigger}
}

// ask files an ask by the asker in the usual thread, of the owners, none with
// a bypass.
func (ledger *slackLedger) ask(trigger string, owners ...string) AskResult {
	ledger.t.Helper()
	return ledger.askIn(inThread(trigger), owners...)
}

func (ledger *slackLedger) askIn(thread SlackThread, owners ...string) AskResult {
	ledger.t.Helper()
	named := make([]AskOwner, len(owners))
	for index, owner := range owners {
		named[index] = AskOwner{UserID: owner, Email: owner + "@example.com", DisplayName: "Name of " + owner}
	}
	return ledger.askOwners(thread, named...)
}

func (ledger *slackLedger) askOwners(thread SlackThread, owners ...AskOwner) AskResult {
	ledger.t.Helper()
	result, err := ledger.service.CreateAsk(context.Background(), AskInput{
		OrganizationID: ledger.org, AskerUserID: asker, Slack: thread, Question: "Where do the dev keys live?", Owners: owners,
	})
	if err != nil {
		ledger.t.Fatalf("CreateAsk: %v", err)
	}
	return result
}

// requestTo is the request of an ask that is addressed to one owner.
func requestTo(t *testing.T, result AskResult, owner string) agents.Interaction {
	t.Helper()
	for _, request := range result.Requests {
		if request.Target.UserID == owner {
			return request
		}
	}
	t.Fatalf("the ask has no request to %s: %+v", owner, result.Requests)
	return agents.Interaction{}
}

// owner is a member in their own signed-in session, the one that decides.
func (ledger *slackLedger) owner(userID string) agents.InteractionActor {
	return agents.InteractionActor{
		OrganizationID: ledger.org, UserID: userID, AgentID: "agt_" + userID, AgentName: "Carl of " + userID,
		AgentKind: "primary", Session: agents.SessionMember,
	}
}

// agentOf is a member's primary agent acting in a run, which is what answers.
func (ledger *slackLedger) agentOf(userID string) agents.InteractionActor {
	actor := ledger.owner(userID)
	actor.Session = agents.SessionAgent
	return actor
}

// read is a request as the ledger stores it.
func (ledger *slackLedger) read(id string) agents.Interaction {
	ledger.t.Helper()
	request, err := ledger.store.Get(context.Background(), id)
	if err != nil {
		ledger.t.Fatalf("Get %s: %v", id, err)
	}
	return request
}

// events are the lifecycle events of a request in the outbox, in the order of
// the revisions they announce, as type:status. The events that name a request
// without being its lifecycle, a lease that was revoked, are not among them.
func (ledger *slackLedger) events(id string) []string {
	ledger.t.Helper()
	rows, err := ledger.database.Pool.Query(context.Background(), `SELECT detail_type || ':' || (detail->>'status')
		FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1 AND detail ? 'status'
		ORDER BY (detail->>'revision')::bigint, detail_type`, id)
	if err != nil {
		ledger.t.Fatal(err)
	}
	defer rows.Close()
	var events []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			ledger.t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		ledger.t.Fatal(err)
	}
	return events
}

// decide is an owner choosing on the web.
func (ledger *slackLedger) decide(owner string, request agents.Interaction, choice string) ConsentResult {
	ledger.t.Helper()
	result, err := ledger.service.ConsentAsOwner(context.Background(), ledger.owner(owner), request.ID, choice)
	if err != nil {
		ledger.t.Fatalf("%s chose %s: %v", owner, choice, err)
	}
	return result
}

// answer is an owner's agent answering an approved request, with a summary
// that says what it is.
func (ledger *slackLedger) answer(request agents.Interaction) (agents.Interaction, error) {
	ledger.t.Helper()
	return ledger.service.Respond(context.Background(), ledger.agentOf(request.Target.UserID), request.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "The keys live in the vault.", Content: "Ask for the dev vault path.",
		ToolsUsed: []string{agents.KnowledgeToolboxName},
	})
}

// count is how many rows a query counts, for a test that checks what the
// database holds.
func (ledger *slackLedger) count(query string, args ...any) int {
	ledger.t.Helper()
	var count int
	if err := ledger.database.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		ledger.t.Fatal(err)
	}
	return count
}

// leases is how many leases the organization has.
func (ledger *slackLedger) leases() int {
	return ledger.count(`SELECT count(*) FROM agents_primary.interaction_leases WHERE organization_id=$1`, ledger.org)
}
