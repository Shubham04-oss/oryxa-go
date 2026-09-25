package interactions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// B's agents, as their own tool calls present them, and member C's.
var (
	askSpecialist  = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_ask", AgentName: "Counsel", AgentKind: "specialist"}
	autoSpecialist = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_b", AgentID: "agt_auto", AgentName: "Researcher", AgentKind: "specialist"}
	otherMember    = agents.InteractionActor{OrganizationID: "org_1", UserID: "user_c", AgentID: "agt_c", AgentName: "Cleo", AgentKind: "primary"}
)

var approve = agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Go ahead."}

// inOwnSession is the member themself, signed in, as the API's member path
// presents them: the only actor DecideAsOwner accepts.
func inOwnSession(actor agents.InteractionActor) agents.InteractionActor {
	actor.Session = agents.SessionMember
	return actor
}

func createInTeamspace(t *testing.T, service *Service, target string) agents.Interaction {
	t.Helper()
	created, err := service.Create(context.Background(), requester, "member-token", target, askInTeamspace(target))
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// TestNoAgentDecidesARequestHeldForItsOwner: neither the addressed agent nor
// its member's primary agent mediating may decide what its owner has to — so
// no tool call does, whatever the agent was asked.
func TestNoAgentDecidesARequestHeldForItsOwner(t *testing.T) {
	service, repo := teamspaceService(t, teamspaceSeats())
	primarySeat := createInTeamspace(t, service, "agt_b")
	askContribution := createInTeamspace(t, service, "agt_ask")
	for _, attempt := range []struct {
		name    string
		actor   agents.InteractionActor
		request string
	}{
		{"the teammate's primary agent, asked itself", targetPrimary, primarySeat.ID},
		{"the specialist, asked itself", askSpecialist, askContribution.ID},
		{"the primary agent, mediating for the specialist", targetPrimary, askContribution.ID},
	} {
		_, err := service.Decide(context.Background(), attempt.actor, attempt.request, approve)
		if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "your member's own decision") {
			t.Fatalf("%s: error=%v, want ErrForbidden saying the member decides", attempt.name, err)
		}
		if status := repo.requests[attempt.request].Status; status != StatusPending {
			t.Fatalf("%s: the request became %s", attempt.name, status)
		}
	}
}

// TestTheOwnerDecidesARequestHeldForThem: B, in their own session, approves
// one request and rejects another; the approved one is then B's agent's to
// carry out and answer, exactly as an approval always was.
func TestTheOwnerDecidesARequestHeldForThem(t *testing.T) {
	service, _ := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	approved, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", held.ID, approve)
	if err != nil || approved.Status != StatusApproved || approved.Decision == nil || approved.Approval != ApprovalOwner {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	completed, err := service.Respond(context.Background(), targetPrimary, held.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Reviewed", Content: "The plan is ready.",
	})
	if err != nil || completed.Status != StatusCompleted {
		t.Fatalf("the agent's answer after its owner approved = %+v, %v", completed, err)
	}

	declined := createInTeamspace(t, service, "agt_ask")
	rejected, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", declined.ID, agents.InteractionDecisionInput{
		Decision: DecisionRejected, Message: "Not this week.",
	})
	if err != nil || rejected.Status != StatusRejected {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
}

// TestOnlyTheAddressedMemberDecidesAsOwner: the requester cannot approve their
// own request by deciding as an owner, and nobody else can read it at all.
func TestOnlyTheAddressedMemberDecidesAsOwner(t *testing.T) {
	service, repo := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	if _, err := service.DecideAsOwner(context.Background(), inOwnSession(requester), "member-token", held.ID, approve); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the requester deciding as owner: error=%v, want ErrForbidden", err)
	}
	if _, err := service.DecideAsOwner(context.Background(), inOwnSession(otherMember), "member-token", held.ID, approve); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another member deciding as owner: error=%v, want ErrNotFound", err)
	}
	if status := repo.requests[held.ID].Status; status != StatusPending {
		t.Fatalf("the request became %s", status)
	}
}

// TestAnAgentStillDecidesWhatItsOwnerDelegatedOrNeverHeld: a specialist
// contributed with automatic delegation decides for itself, and a request
// made outside any teamspace is decided by its agent as before — which its
// member may also do in person.
func TestAnAgentStillDecidesWhatItsOwnerDelegatedOrNeverHeld(t *testing.T) {
	service, _ := teamspaceService(t, teamspaceSeats())
	delegated := createInTeamspace(t, service, "agt_auto")
	if decided, err := service.Decide(context.Background(), autoSpecialist, delegated.ID, approve); err != nil || decided.Status != StatusApproved {
		t.Fatalf("the delegated specialist's own decision = %+v, %v", decided, err)
	}

	outside, err := service.Create(context.Background(), requester, "member-token", "outside", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", Purpose: "Question", Task: "Answer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", outside.ID, approve); err != nil || decided.Status != StatusApproved {
		t.Fatalf("the member deciding a request their agent could = %+v, %v", decided, err)
	}
}

// TestARequestHeldForItsOwnerStillExpires: waiting for a person does not
// keep a request alive past its expiry.
func TestARequestHeldForItsOwnerStillExpires(t *testing.T) {
	service, repo := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	request := repo.requests[held.ID]
	request.ExpiresAt = testNow.Add(-time.Second)
	repo.requests[held.ID] = request
	if read, err := service.Get(context.Background(), targetPrimary, held.ID); err != nil || read.Status != StatusExpired {
		t.Fatalf("read=%+v err=%v, want it expired", read, err)
	}
	if _, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", held.ID, approve); !errors.Is(err, ErrExpired) {
		t.Fatalf("deciding an expired request: error=%v, want ErrExpired", err)
	}
}

// TestAnApprovalNobodyKnowsIsHeldForTheOwner: only an explicit agent
// approval, or none at all from before teamspaces, lets an agent decide;
// anything else fails closed to the member.
func TestAnApprovalNobodyKnowsIsHeldForTheOwner(t *testing.T) {
	for approval, held := range map[string]bool{ApprovalAgent: false, "": false, ApprovalOwner: true, "team": true} {
		if got := HeldForOwner(agents.Interaction{Approval: approval}); got != held {
			t.Errorf("HeldForOwner(%q) = %t, want %t", approval, got, held)
		}
	}
	service, repo := teamspaceService(t, teamspaceSeats())
	unknown := between("irq_unknown", "user_a", "agt_a", "user_b", "agt_b", "tsp_1")
	unknown.Approval = "team"
	repo.requests[unknown.ID] = unknown
	if _, err := service.Decide(context.Background(), targetPrimary, unknown.ID, approve); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an agent deciding a request with an unknown approval: error=%v, want ErrForbidden", err)
	}
	if decided, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", unknown.ID, approve); err != nil || decided.Status != StatusApproved {
		t.Fatalf("its owner deciding it = %+v, %v", decided, err)
	}
}

// TestATeamspaceRequestNamingNoApprovalIsHeld: a request made in a teamspace
// that reaches the ledger naming no approval fails closed to its owner — no
// agent may decide it — while one outside any teamspace naming none is still
// its agent's, as every request before teamspaces was.
func TestATeamspaceRequestNamingNoApprovalIsHeld(t *testing.T) {
	if !HeldForOwner(agents.Interaction{TeamspaceID: "tsp_1"}) || HeldForOwner(agents.Interaction{}) {
		t.Fatal("HeldForOwner: want a teamspace request naming no approval held, and one outside any teamspace not")
	}
	service, repo := teamspaceService(t, teamspaceSeats())
	unnamed := between("irq_unnamed", "user_a", "agt_a", "user_b", "agt_b", "tsp_1")
	unnamed.Approval = ""
	repo.requests[unnamed.ID] = unnamed
	if _, err := service.Decide(context.Background(), targetPrimary, unnamed.ID, approve); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an agent deciding it: error=%v, want ErrForbidden", err)
	}
	if status := repo.requests[unnamed.ID].Status; status != StatusPending {
		t.Fatalf("the request became %s", status)
	}
}

// TestTheStoreRefusesATeamspaceRequestNamingNoApproval: the store fills in
// "agent" only for a request made outside any teamspace; one made in a
// teamspace must say who approves it, and is refused before anything is
// written.
func TestTheStoreRefusesATeamspaceRequestNamingNoApproval(t *testing.T) {
	store := NewStore(nil)
	unnamed := between("irq_unnamed", "user_a", "agt_a", "user_b", "agt_b", "tsp_1")
	unnamed.Approval = ""
	if _, err := store.Create(context.Background(), unnamed, "key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create = %v, want ErrInvalid before the database is reached", err)
	}
}

// TestOnlyTheMembersOwnSessionDecidesAsOwner: calling DecideAsOwner is not
// what makes a decision the member's own — the actor has to come from their
// signed-in session. An agent's run, or an actor that says nothing, is
// refused, and the member's own session never decides as an agent either.
func TestOnlyTheMembersOwnSessionDecidesAsOwner(t *testing.T) {
	service, repo := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	agentRun := targetPrimary
	agentRun.Session = agents.SessionAgent
	for name, actor := range map[string]agents.InteractionActor{"an agent's run": agentRun, "an actor naming no session": targetPrimary} {
		if _, err := service.DecideAsOwner(context.Background(), actor, "member-token", held.ID, approve); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s deciding as owner: error=%v, want ErrForbidden", name, err)
		}
	}
	outside, err := service.Create(context.Background(), requester, "member-token", "outside", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", Purpose: "Question", Task: "Answer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), inOwnSession(targetPrimary), outside.ID, approve); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the member's own session deciding as an agent: error=%v, want ErrForbidden", err)
	}
	if repo.requests[held.ID].Status != StatusPending || repo.requests[outside.ID].Status != StatusPending {
		t.Fatal("a refused decision was recorded")
	}
	if decided, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", held.ID, approve); err != nil || decided.Status != StatusApproved {
		t.Fatalf("the member's own session = %+v, %v", decided, err)
	}
}

// TestADecisionRecordsWhoMadeIt: the member deciding in their own session is
// recorded as the member, with no agent; an agent deciding in a run is
// recorded as that agent, with the member it works for.
func TestADecisionRecordsWhoMadeIt(t *testing.T) {
	service, _ := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	byOwner, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", held.ID, approve)
	if err != nil || byOwner.Decision == nil || byOwner.Decision.DecidedBy == nil ||
		*byOwner.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}) || !DecidedByItsOwner(byOwner) {
		t.Fatalf("the member's own decision = %+v, %v; want decided_by the member", byOwner.Decision, err)
	}
	delegated := createInTeamspace(t, service, "agt_auto")
	byAgent, err := service.Decide(context.Background(), autoSpecialist, delegated.ID, approve)
	if err != nil || byAgent.Decision == nil || byAgent.Decision.DecidedBy == nil ||
		*byAgent.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_auto"}) || DecidedByItsOwner(byAgent) {
		t.Fatalf("the specialist's decision = %+v, %v; want decided_by agt_auto", byAgent.Decision, err)
	}
}
