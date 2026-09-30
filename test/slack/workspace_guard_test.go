package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The Slack switch does not disable the answer guard on existing agent requests.
// This runs the shipped binary with a real applied workspace policy, a lower
// saved preference, a model answering through interactions_respond, and Bedrock
// detecting an email that secrets would otherwise allow through.
func TestSlackOffAgentAnswersHonorWorkspacePeerFloor(t *testing.T) {
	server := os.Getenv("SLACK_TEST_DATABASE_URL")
	if server == "" {
		t.Skip("SLACK_TEST_DATABASE_URL is not set")
	}
	s := bootStackWithWorkspace(t, server, false, []byte(`apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: acme}
preset: team
safety:
  guard:
    holdForReview: false
    pii: {minimum: {peer: strict}}
    relevance: hold
`))
	base := "/v1/organizations/" + organizationID
	status, preferences := s.api(raya, http.MethodPut, base+"/answer-guard/preferences", map[string]string{"peer": "secrets"})
	if status != http.StatusOK || at(preferences, "peer", "chosen") != "secrets" || at(preferences, "peer", "floor") != "strict" || at(preferences, "peer", "effective") != "strict" || at(preferences, "relevance") != "shadow" {
		t.Fatalf("preferences under the workspace floor = %d %v", status, preferences)
	}
	var saved string
	if err := s.database.QueryRow(context.Background(), `SELECT peer_level FROM agents_primary.guard_preferences WHERE organization_id=$1 AND user_id=$2`, organizationID, raya.UserID).Scan(&saved); err != nil || saved != "secrets" {
		t.Fatalf("saved peer choice = %q, %v; want secrets retained", saved, err)
	}
	status, listed := s.api(raya, http.MethodGet, base+"/agents", nil)
	if status != http.StatusOK || len(asSlice(listed["agents"])) != 1 {
		t.Fatalf("the owner's primary agent = %d %v", status, listed)
	}
	target := at(asSlice(listed["agents"])[0], "id")
	const email = "priya@example.com"
	restorePlan := s.vertex.setPlan(func(string) answerPlan {
		return answerPlan{Query: "Q3 contact", Summary: "Write to " + email + ".", Content: "The finance contact is " + email + "."}
	})
	defer restorePlan()
	restoreGuard := s.aws.setGuardrail(func(call guardrailCall) guardrailAnswer {
		if call.Guardrail != "gr_answers" || !strings.Contains(call.Text, email) {
			return defaultGuardrail(call)
		}
		return guardrailAnswer{Status: http.StatusOK, Body: map[string]any{
			"action": "NONE", "outputs": []any{}, "usage": map[string]any{},
			"assessments": []any{map[string]any{"sensitiveInformationPolicy": map[string]any{
				"piiEntities": []any{map[string]any{"type": "EMAIL", "match": email, "action": "NONE", "detected": true}},
			}}},
		}}
	})
	defer restoreGuard()
	status, created := s.api(ada, http.MethodPost, base+"/agent-requests", map[string]any{
		"target_agent_id": target, "purpose": "Finance contact", "task": "Who is the Q3 finance contact?", "requested_tools": []string{"knowledge"},
	})
	id, ok := created["id"].(string)
	if status != http.StatusCreated || !ok || id == "" || created["origin"] != "agent" {
		t.Fatalf("agent request = %d %v", status, created)
	}
	if status, approved := s.api(raya, http.MethodPost, base+"/agent-requests/"+id+"/decision", map[string]any{"decision": "approved", "message": "Share the finance contact.", "allowed_tools": []string{"knowledge"}}); status != http.StatusOK {
		t.Fatalf("owner approval = %d %v", status, approved)
	}
	owner := s.waitForStatus(raya, id, "review")
	if at(owner, "result") != nil || at(owner, "guard", "state") != "held" || fmt.Sprint(at(owner, "guard", "kinds")) != "[EMAIL]" || !strings.Contains(fmt.Sprint(at(owner, "held", "result", "content")), email) || !strings.Contains(fmt.Sprint(at(owner, "held", "masked_result", "content")), "{EMAIL}") {
		t.Fatalf("owner's held answer = %v; want an EMAIL hold and a masked review version", owner)
	}
	requester := s.request(ada, id)
	encoded, err := json.Marshal(requester)
	if err != nil || at(requester, "status") != "review" || at(requester, "result") != nil || at(requester, "held") != nil || strings.Contains(string(encoded), email) {
		t.Fatalf("requester sees %s, %v; want only the review status with no answer", encoded, err)
	}
	var heldOnly bool
	if err := s.database.QueryRow(context.Background(), `SELECT result IS NULL AND held_result IS NOT NULL FROM agents_primary.agent_interactions WHERE id=$1`, id).Scan(&heldOnly); err != nil || !heldOnly {
		t.Fatalf("answer stored only for review = %v, %v", heldOnly, err)
	}
	var events int
	var leaked bool
	if err := s.database.QueryRow(context.Background(), `SELECT count(*), coalesce(bool_or(position($2 in detail::text)>0), false) FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1`, id, email).Scan(&events, &leaked); err != nil || events < 3 || leaked {
		t.Fatalf("request events = %d, contain PII = %v, %v; want requested, approved and review events without the answer", events, leaked, err)
	}
	if messages := s.slack.Messages(); len(messages) != 0 {
		t.Fatalf("with Slack consent off, messages were posted: %+v", messages)
	}
}
