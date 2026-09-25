package interactions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestAnApprovalAllowsOnlyRequestedTools: allowed_tools must be a subset of
// requested_tools, and the toolboxes a request may ask for are spelled as
// the vocabulary spells them whatever case they were sent in, so asking for
// "Knowledge" and allowing "KNOWLEDGE" grants the knowledge toolbox. Allowing
// anything that was not asked for is refused, and nothing is decided.
func TestAnApprovalAllowsOnlyRequestedTools(t *testing.T) {
	service, repo := testService(t)
	created, err := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "Answer.", RequestedTools: []string{"Knowledge", "calendar_lookup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(created.RequestedTools, []string{agents.KnowledgeToolboxName, "calendar_lookup"}) {
		t.Fatalf("requested_tools = %v, want knowledge spelled as the vocabulary does and the other kept as sent", created.RequestedTools)
	}
	for _, allowed := range [][]string{{"apps"}, {"knowledge", "apps"}, {"knowledge_search"}} {
		_, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{
			Decision: DecisionApproved, Message: "Approved", AllowedTools: allowed,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("allowing %v of %v: error=%v, want ErrInvalid", allowed, created.RequestedTools, err)
		}
		if status := repo.requests[created.ID].Status; status != StatusPending {
			t.Fatalf("allowing %v left the request %s", allowed, status)
		}
	}
	approved, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{
		Decision: DecisionApproved, Message: "Approved", AllowedTools: []string{"KNOWLEDGE"},
	})
	if err != nil || !slices.Equal(approved.Decision.AllowedTools, []string{agents.KnowledgeToolboxName}) {
		t.Fatalf("approved=%+v err=%v, want knowledge allowed", approved.Decision, err)
	}
}

// TestAnApprovalThatLeavesOutAllowedToolsAllowsNone: nothing grants a tool
// implicitly. An approval with no allowed_tools records an empty list, which
// the API shows as [], and the answer cannot claim a tool it was not allowed.
func TestAnApprovalThatLeavesOutAllowedToolsAllowsNone(t *testing.T) {
	service, _ := testService(t)
	created, err := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
		TargetAgentID: "agt_engineering", Purpose: "Question", Task: "Answer.",
		RequestedTools: []string{agents.AppsToolboxName, agents.KnowledgeToolboxName},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{Decision: DecisionApproved, Message: "Approved"})
	if err != nil || approved.Decision == nil || approved.Decision.AllowedTools == nil || len(approved.Decision.AllowedTools) != 0 {
		t.Fatalf("approved=%+v err=%v, want an empty allowed_tools", approved.Decision, err)
	}
	encoded, err := json.Marshal(approved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"allowed_tools":[]`) {
		t.Fatalf("the approval reads %s, want allowed_tools []", encoded)
	}
	if _, err := service.Respond(context.Background(), target, created.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "Done", ToolsUsed: []string{agents.AppsToolboxName},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an answer claiming apps after none were allowed: error=%v, want ErrInvalid", err)
	}
}
