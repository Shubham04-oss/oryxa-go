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

func (primarySeat) RequestSeat(_ context.Context, _ agents.TeamspaceActor, _ agents.OrganizationDirectory, _, targetAgentID string) (agents.TeamspaceSeat, error) {
	return agents.TeamspaceSeat{AgentID: targetAgentID, OwnerUserID: "user_b", Kind: "primary"}, nil
}

// RequestStanding finds the seat still there, whenever it is read again.
func (primarySeat) RequestStanding(_ context.Context, _ string, _ agents.OrganizationDirectory, _ string, _, target agents.InteractionParty) (agents.TeamspaceStanding, error) {
	return agents.TeamspaceStanding{Seat: agents.TeamspaceSeat{AgentID: target.AgentID, OwnerUserID: target.UserID, Kind: "primary"}}, nil
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
//
// The waker is handed the deliveries this test recorded rather than claiming
// due ones: the table may be shared with a running agents service, whose
// deliveries a claim here would take and settle with stand-in runs.
// ownerApprovalWorld is the real ledger and delivery store on the test
// database, for one organization no other run shares.
type ownerApprovalWorld struct {
	database   *postgreskit.Database
	ledger     *interactions.Store
	deliveries *Store
	// organizationID is the organization the world's requests are made in.
	organizationID string
	a, b           agents.InteractionActor
}

func newOwnerApprovalWorld(t *testing.T) *ownerApprovalWorld {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-owner-approval-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
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
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_deliveries WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.interaction_delivery_threads WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	})
	return &ownerApprovalWorld{
		database: database, ledger: ledger, deliveries: deliveries, organizationID: organizationID,
		a: agents.InteractionActor{OrganizationID: organizationID, UserID: "user_a", AgentID: "agt_a", AgentName: "Crew", AgentKind: "primary"},
		b: agents.InteractionActor{OrganizationID: organizationID, UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", AgentKind: "primary"},
	}
}

// deliver records the delivery the agents queue makes of the request's
// newest outbox event of one detail type, as internal/inbound does.
func (world *ownerApprovalWorld) deliver(t *testing.T, detailType, kind string, recipient agents.InteractionActor, requestID string) Delivery {
	t.Helper()
	ctx := context.Background()
	var eventID string
	var detail []byte
	if err := world.database.Pool.QueryRow(ctx, `SELECT id, detail FROM agents_primary.agents_outbox
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
	if recorded, err := world.deliveries.Record(ctx, delivery); err != nil || !recorded {
		t.Fatalf("record %s: recorded=%v err=%v", kind, recorded, err)
	}
	return delivery
}

// handled is whether a delivery was completed, rather than dropped or left.
func (world *ownerApprovalWorld) handled(t *testing.T, eventID string) bool {
	t.Helper()
	var done, dropped bool
	if err := world.database.Pool.QueryRow(context.Background(), `SELECT handled_at IS NOT NULL, dropped FROM agents_primary.interaction_deliveries
		WHERE event_id=$1`, eventID).Scan(&done, &dropped); err != nil {
		t.Fatal(err)
	}
	return done && !dropped
}

// memberTokens mints every member an agent token, as auth would for a
// member who allowed agents to act for them while away.
type memberTokens struct{}

func (memberTokens) AgentToken(_ context.Context, userID string) (string, time.Time, error) {
	return "agent-token-" + userID, time.Now().Add(time.Hour), nil
}

func TestAnOwnerHeldRequestLivesItsWholeLifeAgainstTheDatabase(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	a, b := world.a, world.b
	ledger, deliver, handled := world.ledger, func(detailType, kind string, recipient agents.InteractionActor, requestID string) Delivery {
		t.Helper()
		return world.deliver(t, detailType, kind, recipient, requestID)
	}, func(eventID string) bool {
		t.Helper()
		return world.handled(t, eventID)
	}
	// Every read of the request's teamspace, before B's agent is woken or
	// decides, is made as B, with B's agent token.
	requests, err := interactions.New(interactions.Config{Repository: ledger, Directory: teammateB{}, Teamspaces: primarySeat{}, Tokens: memberTokens{}})
	if err != nil {
		t.Fatal(err)
	}

	created, err := requests.Create(ctx, a, "member-token", "lifecycle", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", TeamspaceID: "tsp_launch", Purpose: "Launch", Task: "Review the launch plan.",
	})
	if err != nil || created.Approval != interactions.ApprovalOwner || created.TeamspaceID != "tsp_launch" {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	runs := &fakeAgentRuns{}
	// The ledger reads the request's teamspace again before its agent is
	// woken, as the service wires it.
	waker := NewWaker(world.deliveries, runs, ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)
	requested := deliver(agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, created.ID)
	waker.wake(ctx, requested)
	if len(runs.calls) != 0 || !handled(requested.EventID) {
		t.Fatalf("the request's own delivery: runs=%+v handled=%v, want it completed without waking anyone", runs.calls, handled(requested.EventID))
	}

	approve := agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Go ahead."}
	if _, err := requests.Decide(ctx, b, created.ID, approve); !errors.Is(err, interactions.ErrForbidden) {
		t.Fatalf("B's agent deciding it: error=%v, want ErrForbidden", err)
	}
	bSignedIn := b
	bSignedIn.Session = agents.SessionMember
	if approved, err := requests.DecideAsOwner(ctx, bSignedIn, "member-token", created.ID, approve); err != nil || approved.Status != interactions.StatusApproved {
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
	waker.wake(ctx, approvedDelivery)
	if len(runs.calls) != 1 || !handled(approvedDelivery.EventID) {
		t.Fatalf("the approval's delivery: runs=%+v handled=%v, want one run that answered", runs.calls, handled(approvedDelivery.EventID))
	}
	woken := runs.calls[0]
	if woken.agentID != "agt_b" || woken.member.UserID != "user_b" || woken.reach != managedagents.DeliveryReads ||
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

// lapsingSeat is primarySeat until lapsed names why the request no longer
// stands in its teamspace.
type lapsingSeat struct {
	primarySeat
	lapsed *string
}

func (seat lapsingSeat) RequestStanding(ctx context.Context, organizationID string, directory agents.OrganizationDirectory, id string, requester, target agents.InteractionParty) (agents.TeamspaceStanding, error) {
	if *seat.lapsed != "" {
		return agents.TeamspaceStanding{Lapsed: *seat.lapsed}, nil
	}
	return seat.primarySeat.RequestStanding(ctx, organizationID, directory, id, requester, target)
}

// TestARequestThatLapsesBeforeItsAgentWakesIsClosedNotWorked: B approves
// A's request in person; before the approval wakes B's agent, A leaves the
// teamspace's team. Read again, the request fails with that reason — the
// member who asked hears of it as a failed result — and B's agent, which
// could read B's apps for it, is never woken.
func TestARequestThatLapsesBeforeItsAgentWakesIsClosedNotWorked(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	lapsed := ""
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Teamspaces: lapsingSeat{lapsed: &lapsed}, Tokens: memberTokens{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := requests.Create(ctx, world.a, "member-token", "lapsing", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", TeamspaceID: "tsp_launch", Purpose: "Launch", Task: "Review the launch plan.",
	})
	if err != nil {
		t.Fatal(err)
	}
	bSignedIn := world.b
	bSignedIn.Session = agents.SessionMember
	if _, err := requests.DecideAsOwner(ctx, bSignedIn, "member-token", created.ID, agents.InteractionDecisionInput{
		Decision: interactions.DecisionApproved, Message: "Go ahead.",
	}); err != nil {
		t.Fatal(err)
	}

	lapsed = "the member who asked is no longer on the teamspace's team"
	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)
	approval := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, world.b, created.ID)
	waker.wake(ctx, approval)
	if len(runs.calls) != 0 || !world.handled(t, approval.EventID) {
		t.Fatalf("the approval's delivery: runs=%+v handled=%v, want it done without waking B's agent", runs.calls, world.handled(t, approval.EventID))
	}
	stored, err := world.ledger.Get(ctx, created.ID)
	if err != nil || stored.Status != interactions.StatusFailed || stored.Result == nil || !strings.Contains(stored.Result.Summary, lapsed) ||
		!interactions.DecidedByItsOwner(stored) {
		t.Fatalf("stored=%+v err=%v, want it failed saying why, B's own approval kept", stored, err)
	}
	if report := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, world.a, created.ID); report.Status != interactions.StatusFailed {
		t.Fatalf("A's report = %+v, want the failed result", report)
	}
}

// TestAPendingRequestHeldForItsOwnerWakesNobody: a pending request its agent
// could decide is held for its owner, which the ledger records as a new
// requested event. That event is delivered to the addressed agent as any
// requested event is, and completed without a run: nothing about the request
// is the agent's to act on any more.
func TestAPendingRequestHeldForItsOwnerWakesNobody(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := agents.Interaction{
		ID: world.organizationID + "_held", OrganizationID: world.organizationID,
		TeamspaceID: "tsp_launch", Approval: interactions.ApprovalAgent,
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl"},
		Purpose:   "Launch", Task: "Review the launch plan.",
		Context: []agents.InteractionContextItem{}, RequestedTools: []string{}, RequestedPermissions: []string{},
		Status: interactions.StatusPending, Revision: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := world.ledger.Create(ctx, request, "held"); err != nil {
		t.Fatal(err)
	}
	if held, err := world.ledger.Hold(ctx, request.ID, now.Add(time.Second)); err != nil || held.Approval != interactions.ApprovalOwner {
		t.Fatalf("held = %+v, %v", held, err)
	}
	requests, err := interactions.New(interactions.Config{Repository: world.ledger, Directory: teammateB{}, Teamspaces: primarySeat{}, Tokens: memberTokens{}})
	if err != nil {
		t.Fatal(err)
	}
	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)

	hold := world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, world.b, request.ID)
	if hold.Revision != 2 {
		t.Fatalf("delivered %+v, want the hold's requested event, at revision 2", hold)
	}
	waker.wake(ctx, hold)
	if len(runs.calls) != 0 || !world.handled(t, hold.EventID) {
		t.Fatalf("the hold's delivery: runs=%+v handled=%v, want it completed without waking anyone", runs.calls, world.handled(t, hold.EventID))
	}
}

// dropped is whether a delivery was dropped rather than completed.
func (world *ownerApprovalWorld) dropped(t *testing.T, eventID string) bool {
	t.Helper()
	var handled, dropped bool
	if err := world.database.Pool.QueryRow(context.Background(), `SELECT handled_at IS NOT NULL, dropped FROM agents_primary.interaction_deliveries
		WHERE event_id=$1`, eventID).Scan(&handled, &dropped); err != nil {
		t.Fatal(err)
	}
	return handled && dropped
}

// TestApprovedWorkThatKeepsFailingIsClosedAndTheRequesterTold: B's agent
// approves A's request, and every run woken to do the work fails. After the
// last attempt the delivery is dropped and the request failed, saying why,
// rather than left approved for good; its completed event reaches A as any
// failed result does.
func TestApprovedWorkThatKeepsFailingIsClosedAndTheRequesterTold(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	requests, err := interactions.New(interactions.Config{Repository: world.ledger, Directory: teammateB{}, Teamspaces: primarySeat{}, Tokens: memberTokens{}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := requests.Create(ctx, world.a, "member-token", "failing", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", Purpose: "Launch", Task: "Review the launch plan.",
	})
	if err != nil || created.Approval != interactions.ApprovalAgent {
		t.Fatalf("created = %+v, %v", created, err)
	}
	if _, err := requests.Decide(ctx, world.b, created.ID, agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Go ahead."}); err != nil {
		t.Fatal(err)
	}

	waker := NewWaker(world.deliveries, &fakeAgentRuns{fail: true}, world.ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)
	approval := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, world.b, created.ID)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		waker.wake(ctx, approval)
		stored, err := world.ledger.Get(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < maxAttempts && (stored.Status != interactions.StatusApproved || world.dropped(t, approval.EventID)) {
			t.Fatalf("after attempt %d: %s, dropped %t; want it approved and retried", attempt, stored.Status, world.dropped(t, approval.EventID))
		}
	}
	if !world.dropped(t, approval.EventID) {
		t.Fatal("the approval's delivery was not dropped after its last attempt")
	}
	stored, err := world.ledger.Get(ctx, created.ID)
	if err != nil || stored.Status != interactions.StatusFailed || stored.Result == nil || stored.Result.Summary != closedAfterAttempts {
		t.Fatalf("stored = %+v, %v; want it failed saying why", stored, err)
	}
	if report := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, world.a, created.ID); report.Status != interactions.StatusFailed {
		t.Fatalf("A's report = %+v, want the failed result", report)
	}
}

// TestAnApprovalThatRanOutIsClosedOnThePoll: B's agent approved A's request,
// and nobody answered it before it expired, whatever became of its delivery.
// The waker's poll fails it, saying so, and its completed event reaches A.
// The poll closes every approval that ran out in the table, so this runs on
// a database no agents service is working from.
func TestAnApprovalThatRanOutIsClosedOnThePoll(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	made := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	request := agents.Interaction{
		ID: world.organizationID + "_ran_out", OrganizationID: world.organizationID, Approval: interactions.ApprovalAgent,
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl"},
		Purpose:   "Launch", Task: "Review the launch plan.",
		Context: []agents.InteractionContextItem{}, RequestedTools: []string{}, RequestedPermissions: []string{},
		Status: interactions.StatusPending, Revision: 1, ExpiresAt: made.Add(time.Hour), CreatedAt: made, UpdatedAt: made,
	}
	if _, err := world.ledger.Create(ctx, request, "ran-out"); err != nil {
		t.Fatal(err)
	}
	if _, err := world.ledger.Decide(ctx, request.ID, agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Go ahead."},
		agents.InteractionDecider{Kind: interactions.DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}, made.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)
	waker.tidy(ctx)
	stored, err := world.ledger.Get(ctx, request.ID)
	if err != nil || stored.Status != interactions.StatusFailed || stored.Result == nil || stored.Result.Summary != closedExpired || len(runs.calls) != 0 {
		t.Fatalf("stored = %+v, %v, runs %d; want it failed saying it ran out, and nothing run", stored, err, len(runs.calls))
	}
	if report := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, world.a, request.ID); report.Status != interactions.StatusFailed {
		t.Fatalf("A's report = %+v, want the failed result", report)
	}
}
