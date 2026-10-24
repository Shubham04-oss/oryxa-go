package threads

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// threadEvents is everything stored in the member's thread.
func (h *harness) threadEvents(threadID string) []*session.Event {
	h.t.Helper()
	got, err := h.sessions.Get(context.Background(), &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: threadID})
	if err != nil {
		h.t.Fatal(err)
	}
	return sessionEvents(got.Session)
}

// store appends events to the member's thread as an earlier run left them.
func (h *harness) store(threadID string, stored ...*session.Event) {
	h.t.Helper()
	got, err := h.sessions.Get(context.Background(), &session.GetRequest{AppName: "krude", UserID: asha.UserID, SessionID: threadID})
	if err != nil {
		h.t.Fatal(err)
	}
	for _, event := range stored {
		if err := h.sessions.AppendEvent(context.Background(), got.Session, event); err != nil {
			h.t.Fatal(err)
		}
	}
}

func storedEvent(author string, content *genai.Content) *session.Event {
	event := session.NewEvent(context.Background(), "cut-run")
	event.Author, event.Content = author, content
	return event
}

// interruptedRepairs are the events the threads stored to answer calls as
// interrupted.
func interruptedRepairs(events []*session.Event) []*session.Event {
	var repairs []*session.Event
	for _, event := range events {
		if event.InvocationID == primaryagents.InterruptedCallsInvocation {
			repairs = append(repairs, event)
		}
	}
	return repairs
}

// interruptedResponse is the response a request carries for the named call
// that says it was interrupted, if it carries one.
func interruptedResponse(request *model.LLMRequest, name string) *genai.FunctionResponse {
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if response := part.FunctionResponse; response != nil && response.Name == name {
				if text, _ := response.Response["error"].(string); strings.Contains(text, "interrupted") {
					return response
				}
			}
		}
	}
	return nil
}

// A run cut off after Krude asked to send a message, and before the send
// answered, leaves the call unanswered. The next run tells the model the
// call was interrupted, so it checks before sending again, and stores that
// once: a later run adds nothing.
func TestTheRunAfterACutOffCallIsToldTheCallWasInterrupted(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []*model.LLMRequest
	)
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		return genai.NewContentFromText("I'll check whether it went before sending it again.", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	h.store(created.ID,
		storedEvent("user", genai.NewContentFromText("Send Sam the notes.", genai.RoleUser)),
		storedEvent("krude", &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "adk-send-1", Name: "send_message", Args: map[string]any{"to": "sam"},
		}}}}),
	)

	items, _ := h.run(asha, created.ID, RunInput{Message: "Did it go?"})

	if len(requests) != 1 {
		t.Fatalf("the model was called %d times, want once", len(requests))
	}
	if interruptedResponse(requests[0], "send_message") == nil {
		t.Fatalf("the model was not told the send was interrupted: %+v", requests[0].Contents)
	}
	calls := 0
	for _, content := range requests[0].Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.Name == "send_message" {
				calls++
			}
		}
	}
	if calls != 1 {
		t.Fatalf("the model was sent the call %d times, want once, beside its interrupted response", calls)
	}
	if answers := find(items, ItemMessage); len(answers) == 0 {
		t.Fatalf("no answer: %+v", items)
	}

	events := h.threadEvents(created.ID)
	repairs := interruptedRepairs(events)
	if len(repairs) != 1 || repairs[0].Author != "krude" || len(repairs[0].Content.Parts) != 1 ||
		repairs[0].Content.Parts[0].FunctionResponse.ID != "adk-send-1" {
		t.Fatalf("repairs = %+v, want one krude response to adk-send-1", repairs)
	}
	thread, err := h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	results := find(thread.Items, ItemToolResult)
	if len(results) != 1 || !results[0].Failed || results[0].Tool != "send_message" || !strings.Contains(results[0].Error, "interrupted") {
		t.Fatalf("history results = %+v, want the send shown as interrupted", results)
	}

	h.run(asha, created.ID, RunInput{Message: "And now?"})
	if repairs := interruptedRepairs(h.threadEvents(created.ID)); len(repairs) != 1 {
		t.Fatalf("after another run the thread holds %d repairs, want the one", len(repairs))
	}
	if interruptedResponse(requests[1], "send_message") == nil {
		t.Fatal("the next run's model call no longer says the send was interrupted")
	}
}

// A tool waiting for the member's confirmation is not interrupted: its
// confirmation is left for the member, and once they approve it, the tool
// runs as it always did.
func TestACallWaitingForConfirmationIsNotAnsweredAsInterrupted(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Deleted it.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"},
		}}}}, nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := h.run(asha, created.ID, RunInput{Message: "Delete plan.txt"})
	confirmations := find(items, ItemConfirmation)
	if len(confirmations) != 1 {
		t.Fatalf("confirmations = %+v", confirmations)
	}

	waiting := h.threadEvents(created.ID)
	if repairs := primaryagents.InterruptedCallEvents(context.Background(), waiting); len(repairs) != 0 {
		t.Fatalf("a thread waiting on a confirmation would get %d interrupted responses", len(repairs))
	}

	h.run(asha, created.ID, RunInput{Confirmations: []Answer{{ID: confirmations[0].CallID, Confirmed: true}}})
	if !slices.Equal(*h.deleted, []string{"plan.txt"}) {
		t.Fatalf("deleted = %v, want the approved call to run once", *h.deleted)
	}
	if repairs := interruptedRepairs(h.threadEvents(created.ID)); len(repairs) != 0 {
		t.Fatalf("the thread holds %d interrupted responses, want none", len(repairs))
	}
}
