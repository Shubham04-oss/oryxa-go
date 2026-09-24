package interactiondelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// primarySeat seats every target in its owner's whisperer seat, as the
// teamspace does a teammate's primary agent.
type primarySeat struct{}

func (primarySeat) RequestSeat(_ context.Context, _ agents.TeamspaceActor, _, _, targetAgentID string) (agents.TeamspaceSeat, error) {
	return agents.TeamspaceSeat{AgentID: targetAgentID, OwnerUserID: "user_b", Kind: "primary"}, nil
}

// teammateB is A's view of the organization: B and B's primary agent. A new
// request only loads it.
type teammateB struct{ agents.Directory }

func (teammateB) Load(context.Context, string, string) (agents.OrganizationDirectory, error) {
	return agents.OrganizationDirectory{People: []agents.DirectoryPerson{{
		UserID: "user_b", DisplayName: "B",
		Agents: []agents.DirectoryAgent{{ID: "agt_b", OwnerUserID: "user_b", Kind: "primary", Name: "Carl"}},
	}}}, nil
}

// TestAnOwnerHeldRequestLivesItsWholeLifeAgainstTheDatabase runs a request A
// makes of B's primary agent in a teamspace through the real ledger, its
// outbox events, the delivery store and the waker; only the teamspace seat,
// the directory and the agent runs are stand-ins. The request's delivery
// wakes nobody, B's agent cannot decide it, B does, and only then is B's
// agent woken — able to read B's apps and told B approved — to answer it.
func TestAnOwnerHeldRequestLivesItsWholeLifeAgainstTheDatabase(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-owner-approval-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ledger := interactions.NewStore(database)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries := NewStore(database)
	if err := deliveries.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	organizationID := fmt.Sprintf("org_owner_approval_test_%d", time.Now().UnixNano())
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_deliveries WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_delivery_threads WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()
	requests, err := interactions.New(interactions.Config{Repository: ledger, Directory: teammateB{}, Teamspaces: primarySeat{}})
	if err != nil {
		t.Fatal(err)
	}
	a := agents.InteractionActor{OrganizationID: organizationID, UserID: "user_a", AgentID: "agt_a", AgentName: "Crew", AgentKind: "primary"}
	b := agents.InteractionActor{OrganizationID: organizationID, UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", AgentKind: "primary"}

	// deliver records the delivery the agents queue makes of the request's
	// newest outbox event of one detail type, as internal/inbound does.
	deliver := func(detailType, kind string, recipient agents.InteractionActor, requestID string) Delivery {
		t.Helper()
		var eventID string
		var detail []byte
		if err := database.Pool.QueryRow(ctx, `SELECT id, detail FROM agents_primary.agents_outbox
			WHERE detail_type=$1 AND detail->>'request_id'=$2 ORDER BY occurred_at DESC LIMIT 1`, detailType, requestID).Scan(&eventID, &detail); err != nil {
			t.Fatalf("read the %s event: %v", detailType, err)
		}
		var changed agentsv1.InteractionChanged
		if err := json.Unmarshal(detail, &changed); err != nil {
			t.Fatal(err)
		}
		delivery := Delivery{
			EventID: eventID, RequestID: changed.RequestID, OrganizationID: changed.OrganizationID,
			RecipientUserID: recipient.UserID, RecipientAgentID: recipient.AgentID, Kind: kind,
			Status: changed.Status, Revision: changed.Revision, ChangedAt: changed.ChangedAt,
		}
		if recorded, err := deliveries.Record(ctx, delivery); err != nil || !recorded {
			t.Fatalf("record %s: recorded=%v err=%v", kind, recorded, err)
		}
		return delivery
	}
	handled := func(eventID string) bool {
		t.Helper()
		var done, dropped bool
		if err := database.Pool.QueryRow(ctx, `SELECT handled_at IS NOT NULL, dropped FROM agents_primary.interaction_deliveries
			WHERE event_id=$1`, eventID).Scan(&done, &dropped); err != nil {
			t.Fatal(err)
		}
		return done && !dropped
	}

	created, err := requests.Create(ctx, a, "member-token", "lifecycle", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", TeamspaceID: "tsp_launch", Purpose: "Launch", Task: "Review the launch plan.",
	})
	if err != nil || created.Approval != interactions.ApprovalOwner || created.TeamspaceID != "tsp_launch" {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(deliveries, runs, ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)
	requested := deliver(agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, created.ID)
	if err := waker.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || !handled(requested.EventID) {
		t.Fatalf("the request's own delivery: runs=%+v handled=%v, want it completed without waking anyone", runs.calls, handled(requested.EventID))
	}

	approve := agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Go ahead."}
	if _, err := requests.Decide(ctx, b, created.ID, approve); !errors.Is(err, interactions.ErrForbidden) {
		t.Fatalf("B's agent deciding it: error=%v, want ErrForbidden", err)
	}
	if approved, err := requests.DecideAsOwner(ctx, b, created.ID, approve); err != nil || approved.Status != interactions.StatusApproved {
		t.Fatalf("B deciding it = %+v, %v", approved, err)
	}

	runs.onExecute = func() {
		if _, err := requests.Respond(ctx, b, created.ID, agents.InteractionResponseInput{
			Status: interactions.StatusCompleted, Summary: "Reviewed", Content: "The launch plan is ready.",
		}); err != nil {
			t.Errorf("B's agent answering the approved request: %v", err)
		}
	}
	approvedDelivery := deliver(agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, created.ID)
	if err := waker.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || !handled(approvedDelivery.EventID) {
		t.Fatalf("the approval's delivery: runs=%+v handled=%v, want one run that answered", runs.calls, handled(approvedDelivery.EventID))
	}
	woken := runs.calls[0]
	if woken.agentID != "agt_b" || woken.member.UserID != "user_b" || woken.reach != managedagents.UnattendedReads ||
		!strings.Contains(woken.input.Message, "approved this request themself") {
		t.Fatalf("woken=%+v, want B's agent reading B's apps, told B approved", woken)
	}

	between, err := requests.List(ctx, a, interactions.Query{Box: interactions.BoxAll, TeamspaceID: "tsp_launch", WithUserID: "user_b"})
	if err != nil || len(between) != 1 || between[0].ID != created.ID || between[0].Status != interactions.StatusCompleted || between[0].Result == nil {
		t.Fatalf("what A sees between A and B = %+v, %v; want the answered request", between, err)
	}
	fromB, err := requests.List(ctx, b, interactions.Query{Box: interactions.BoxAll, TeamspaceID: "tsp_launch", WithUserID: "user_a"})
	if err != nil || len(fromB) != 1 || fromB[0].ID != created.ID {
		t.Fatalf("what B sees between B and A = %+v, %v", fromB, err)
	}
}
