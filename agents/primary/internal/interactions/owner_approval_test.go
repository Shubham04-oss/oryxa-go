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
	approved, err := service.DecideAsOwner(context.Background(), targetPrimary, held.ID, approve)
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
	rejected, err := service.DecideAsOwner(context.Background(), targetPrimary, declined.ID, agents.InteractionDecisionInput{
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
	if _, err := service.DecideAsOwner(context.Background(), requester, held.ID, approve); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the requester deciding as owner: error=%v, want ErrForbidden", err)
	}
	if _, err := service.DecideAsOwner(context.Background(), otherMember, held.ID, approve); !errors.Is(err, ErrNotFound) {
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
	if decided, err := service.DecideAsOwner(context.Background(), targetPrimary, outside.ID, approve); err != nil || decided.Status != StatusApproved {
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
	if _, err := service.DecideAsOwner(context.Background(), targetPrimary, held.ID, approve); !errors.Is(err, ErrExpired) {
		t.Fatalf("deciding an expired request: error=%v, want ErrExpired", err)
	}
}
