package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

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
