package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// DecideAsOwner is the one way the server decides: as the member themself.
func (service *fakeInteractions) DecideAsOwner(ctx context.Context, actor primaryagents.InteractionActor, id string, input primaryagents.InteractionDecisionInput) (primaryagents.Interaction, error) {
	return service.Decide(ctx, actor, id, input)
}

// teamspaceInteractions is fakeInteractions keeping the last request body it
// was asked to create.
type teamspaceInteractions struct {
	fakeInteractions
	input   primaryagents.InteractionCreateInput
	creates int
}

func (service *teamspaceInteractions) Create(ctx context.Context, actor primaryagents.InteractionActor, token, key string, input primaryagents.InteractionCreateInput) (primaryagents.Interaction, error) {
	service.input = input
	service.creates++
	return service.fakeInteractions.Create(ctx, actor, token, key, input)
}

func agentRequestServer(interactions AgentInteractions) *Server {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}}
	return New(member(), &fakeThreads{}, Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: managed, Interactions: interactions,
	}, nil)
}

// TestARequestNamesItsTeamspaceButNeverWhoApprovesIt: the teamspace reaches
// the ledger, which decides who approves from the target's standing there; a
// body that tries to choose that itself is refused before it gets that far.
func TestARequestNamesItsTeamspaceButNeverWhoApprovesIt(t *testing.T) {
	interactions := &teamspaceInteractions{}
	server := agentRequestServer(interactions)
	created := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests",
		`{"target_agent_id":"agt_b","teamspace_id":"tsp_1","purpose":"Launch","task":"Review the plan."}`, bearer)
	if created.Code != http.StatusCreated || interactions.input.TeamspaceID != "tsp_1" || interactions.input.TargetAgentID != "agt_b" {
		t.Fatalf("create = %d %s, input=%+v", created.Code, created.Body.String(), interactions.input)
	}
	chosen := serve(server, http.MethodPost, "/v1/organizations/org_1/agent-requests",
		`{"target_agent_id":"agt_b","teamspace_id":"tsp_1","approval":"agent","purpose":"Launch","task":"Review the plan."}`, bearer)
	if chosen.Code != http.StatusBadRequest || errorCode(t, chosen) != "invalid_request" || interactions.creates != 1 {
		t.Fatalf("a body choosing its approval = %d %s after %d creates, want 400 and no second create", chosen.Code, chosen.Body.String(), interactions.creates)
	}
}

// heldRequests is a request ledger holding one request. Reading and deciding
// it is all a decision reaches; the embedded nil Repository fails loudly on
// anything else.
type heldRequests struct {
	interactions.Repository
	request primaryagents.Interaction
}

func (ledger *heldRequests) Get(_ context.Context, id string) (primaryagents.Interaction, error) {
	if id != ledger.request.ID {
		return primaryagents.Interaction{}, interactions.ErrNotFound
	}
	return ledger.request, nil
}

func (ledger *heldRequests) Decide(_ context.Context, _ string, input primaryagents.InteractionDecisionInput, at time.Time) (primaryagents.Interaction, error) {
	ledger.request.Status = interactions.StatusRejected
	if input.Decision == interactions.DecisionApproved {
		ledger.request.Status = interactions.StatusApproved
	}
	ledger.request.Decision = &primaryagents.InteractionDecision{Decision: input.Decision, Message: input.Message, DecidedAt: at}
	ledger.request.Revision++
	return ledger.request, nil
}

// unusedDirectory stands in for the organization directory, which deciding
// never reads.
type unusedDirectory struct{ primaryagents.Directory }

// TestOnlyTheMembersOwnSessionDecidesARequestHeldForThem runs the real
// ledger service behind the API. Grace asked user_1's primary agent for
// something in a teamspace, so user_1 decides: the agent's own tool path is
// refused, while user_1, signed in, approves it through the decision route.
func TestOnlyTheMembersOwnSessionDecidesARequestHeldForThem(t *testing.T) {
	ledger := &heldRequests{request: primaryagents.Interaction{
		ID: "irq_held", OrganizationID: "org_1", TeamspaceID: "tsp_1", Approval: interactions.ApprovalOwner,
		Requester: primaryagents.InteractionParty{UserID: "user_2", AgentID: "agt_grace", AgentName: "Cleo"},
		Target:    primaryagents.InteractionParty{UserID: "user_1", AgentID: "agt_primary", AgentName: "Carl"},
		Purpose:   "Launch", Task: "Review the plan.", Status: interactions.StatusPending, Revision: 1,
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	requests, err := interactions.New(interactions.Config{Repository: ledger, Directory: unusedDirectory{}})
	if err != nil {
		t.Fatal(err)
	}

	agent := primaryagents.InteractionActor{OrganizationID: "org_1", UserID: "user_1", AgentID: "agt_primary", AgentName: "Carl", AgentKind: "primary"}
	if _, err := requests.Decide(context.Background(), agent, "irq_held", primaryagents.InteractionDecisionInput{
		Decision: interactions.DecisionApproved, Message: "Approved.",
	}); !errors.Is(err, interactions.ErrForbidden) || ledger.request.Status != interactions.StatusPending {
		t.Fatalf("the agent's own decision: error=%v status=%s, want ErrForbidden and nothing decided", err, ledger.request.Status)
	}

	decided := serve(agentRequestServer(requests), http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_held/decision",
		`{"decision":"approved","message":"Go ahead."}`, bearer)
	var body primaryagents.Interaction
	if err := json.Unmarshal(decided.Body.Bytes(), &body); err != nil {
		t.Fatalf("decision = %d %s: %v", decided.Code, decided.Body.String(), err)
	}
	if decided.Code != http.StatusOK || body.Status != interactions.StatusApproved || body.Approval != interactions.ApprovalOwner || body.TeamspaceID != "tsp_1" {
		t.Fatalf("the member's own decision = %d %s, want 200 and an approved request held for its owner in tsp_1", decided.Code, decided.Body.String())
	}
}
