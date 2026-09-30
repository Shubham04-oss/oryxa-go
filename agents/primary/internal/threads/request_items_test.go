package threads

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestRequestCreationAddsOnlyAPointerInHistoryAndStream(t *testing.T) {
	event := &session.Event{ID: "event_create", Author: "Leo"}
	event.Content = &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: "call_create", Name: "interactions_create", Response: map[string]any{
			"id": "irq_created", "status": "pending", "task": "private question",
			"held": map[string]any{"result": map[string]any{"content": "private answer"}},
		},
	}}}}
	for _, items := range [][]Item{historyItems([]*session.Event{event}), streamItems(event)} {
		if len(items) != 2 || items[0].Type != ItemToolResult || items[1].Type != ItemRequest ||
			items[1].RequestID != "irq_created" || items[1].Status != "pending" || items[1].CallID != "call_create" {
			t.Fatalf("items=%+v", items)
		}
		encoded, err := json.Marshal(items)
		if err != nil || strings.Contains(string(encoded), "private") {
			t.Fatalf("pointer leaked tool response: %s err=%v", encoded, err)
		}
	}
}

func TestFailedOrUnrelatedToolResultsAddNoRequestPointer(t *testing.T) {
	for _, response := range []*genai.FunctionResponse{
		{Name: "interactions_create", Response: map[string]any{"error": "not filed", "id": "irq_bad"}},
		{Name: "interactions_get", Response: map[string]any{"id": "irq_other"}},
		{Name: "interactions_create", Response: map[string]any{}},
	} {
		event := &session.Event{ID: "event", Author: "Leo"}
		event.Content = &genai.Content{Parts: []*genai.Part{{FunctionResponse: response}}}
		items := streamItems(event)
		if len(items) != 1 || items[0].Type != ItemToolResult {
			t.Fatalf("%s: items=%+v", response.Name, items)
		}
	}
}
