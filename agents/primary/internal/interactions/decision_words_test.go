package interactions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestTheMemberWhoAskedReadsAnAgentsDecisionInFixedWords: whatever an agent
// writes when it decides, which no guard reads, the member who asked reads
// that it approved or declined, in fixed words, and nothing it wrote; a
// member deciding in their own session is read in their own words.
func TestTheMemberWhoAskedReadsAnAgentsDecisionInFixedWords(t *testing.T) {
	for decision, want := range map[string]string{DecisionRejected: AgentDeclined, DecisionApproved: AgentApproved} {
		service, _ := testService(t)
		created, err := service.Create(context.Background(), requester, "token", "key", agents.InteractionCreateInput{
			TargetAgentID: "agt_engineering", Purpose: "Contact", Task: "How do I reach Ann?",
		})
		if err != nil {
			t.Fatal(err)
		}
		decided, err := service.Decide(context.Background(), target, created.ID, agents.InteractionDecisionInput{
			Decision: decision, Message: "Ann's phone is 555-0100 and she lives at 100 Main Street.",
		})
		if err != nil || decided.Decision == nil || decided.Decision.Message != want {
			t.Fatalf("%s: decided=%+v err=%v, want the message %q", decision, decided.Decision, err, want)
		}
		read, err := service.Get(context.Background(), requester, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(read)
		if strings.Contains(string(encoded), "555-0100") || strings.Contains(string(encoded), "Main Street") {
			t.Fatalf("%s: the member who asked reads %s", decision, encoded)
		}
	}

	service, _ := teamspaceService(t, teamspaceSeats())
	held := createInTeamspace(t, service, "agt_b")
	decided, err := service.DecideAsOwner(context.Background(), inOwnSession(targetPrimary), "member-token", held.ID, agents.InteractionDecisionInput{
		Decision: DecisionRejected, Message: "Not this week, sorry.",
	})
	if err != nil || decided.Decision.Message != "Not this week, sorry." {
		t.Fatalf("the member's own decision = %+v, %v; want their own words", decided.Decision, err)
	}
}
