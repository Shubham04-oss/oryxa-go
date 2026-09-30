package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
)

func TestToolResultErrorsInHistoryAndStream(t *testing.T) {
	for _, test := range []struct {
		name     string
		response map[string]any
		want     string
	}{
		{name: "no error", response: map[string]any{"result": "found"}},
		{name: "null error", response: map[string]any{"error": nil, "result": "found"}},
		{name: "empty error", response: map[string]any{"error": "", "result": "found"}},
		{name: "whitespace error", response: map[string]any{"error": " \n\t ", "result": "found"}},
		{name: "MCP output", response: map[string]any{"output": map[string]any{"successful": true, "data": map[string]any{"error": "a document's text"}}}},
		{name: "tool failure", response: map[string]any{"error": " access refused "}, want: "access refused"},
		{name: "structured failure", response: map[string]any{"error": map[string]any{"code": "forbidden", "message": "access refused"}}, want: `{"code":"forbidden","message":"access refused"}`},
		{name: "bounded failure", response: map[string]any{"error": strings.Repeat("界", maxErrorRunes+1)}, want: strings.Repeat("界", maxErrorRunes) + "…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := &session.Event{ID: "event_result", Author: "Leo"}
			event.Content = &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				ID: "call_read", Name: "knowledge_search", Response: test.response,
			}}}}
			for source, items := range map[string][]Item{"history": historyItems([]*session.Event{event}), "stream": streamItems(event)} {
				if len(items) != 1 || items[0].Type != ItemToolResult || items[0].CallID != "call_read" ||
					items[0].Failed != (test.want != "") || items[0].Error != test.want {
					t.Fatalf("%s result = %+v, want error %q", source, items, test.want)
				}
			}
		})
	}
}

func TestRecoveredToolFailureSurvivesASuccessfulAnswer(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("The launch is 14 October.", genai.RoleModel), nil
		}
		// The harness has no such tool. ADK records a tool error and lets the
		// model recover, so the run itself can complete without run_failed.
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_unknown", Name: "unknown_tool",
		}}}}, nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, run := h.run(asha, created.ID, RunInput{Message: "When is the launch?"})
	if h.runs.finished[run.ID()].Status != runstore.StatusCompleted || len(find(items, ItemRunFailed)) != 0 {
		t.Fatalf("run outcome = %+v, items = %+v", h.runs.finished[run.ID()], items)
	}
	thread, err := h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for source, items := range map[string][]Item{"live": items, "history": thread.Items} {
		results := find(items, ItemToolResult)
		if len(results) != 1 || !results[0].Failed || results[0].Error == "" {
			t.Fatalf("%s results = %+v, want the actual tool failure", source, results)
		}
		messages := find(items, ItemMessage)
		if messages[len(messages)-1].Text != "The launch is 14 October." {
			t.Fatalf("%s answer = %+v", source, messages)
		}
	}
}

func TestTwelveSuccessfulToolResultsStaySuccessfulAfterReload(t *testing.T) {
	events := []*session.Event{}
	for index := range 12 {
		callID := fmt.Sprintf("call_%d", index)
		call := &session.Event{ID: callID, Author: "Leo"}
		call.Content = &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: callID, Name: "knowledge_search",
		}}}}
		result := &session.Event{ID: "result_" + callID, Author: "Leo"}
		result.Content = &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: callID, Name: "knowledge_search", Response: map[string]any{"error": "", "result": "private source"},
		}}}}
		events = append(events, call, result)
	}
	answer := &session.Event{ID: "answer", Author: "Leo"}
	answer.Content = genai.NewContentFromText("The launch is 14 October.", genai.RoleModel)
	events = append(events, answer)

	var live []Item
	for _, event := range events {
		live = append(live, streamItems(event)...)
	}
	// Session storage round-trips the result maps through JSON. History and
	// live SSE must report the same outcome, without exposing source content.
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	var restored []*session.Event
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	for source, items := range map[string][]Item{"live": live, "reloaded": historyItems(restored)} {
		results := find(items, ItemToolResult)
		if len(results) != 12 {
			t.Fatalf("%s results = %d, want 12", source, len(results))
		}
		for _, result := range results {
			if result.Failed || result.Error != "" {
				t.Fatalf("%s result = %+v, want no failure", source, result)
			}
		}
		if messages := find(items, ItemMessage); len(messages) != 1 || messages[0].Text != "The launch is 14 October." {
			t.Fatalf("%s answer = %+v", source, messages)
		}
		encoded, err := json.Marshal(items)
		if err != nil || strings.Contains(string(encoded), "private source") {
			t.Fatalf("%s items leaked the tool result: %s, err=%v", source, encoded, err)
		}
	}
}
