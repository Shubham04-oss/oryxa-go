package threads

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
)

func TestPendingConfirmationsCountsEveryChatFromItsState(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Deleted it.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"},
		}}}}, nil
	})
	ctx := context.Background()
	waiting, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Create(ctx, asha); err != nil {
		t.Fatal(err)
	}
	items, _ := h.run(asha, waiting.ID, RunInput{Message: "Delete plan.txt"})
	confirmations := find(items, ItemConfirmation)
	if len(confirmations) != 1 {
		t.Fatalf("confirmations = %+v", confirmations)
	}

	// The run left the count in the thread's state, so the list reads no history.
	stored, err := h.service.readThread(ctx, asha, waiting.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := stateInt(stored.State(), stateConfirmationsPending); !ok || count != 1 {
		t.Fatalf("state count = %d, %v; want 1", count, ok)
	}
	pending, err := h.service.PendingConfirmations(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != waiting.ID || pending[0].PendingConfirmations != 1 || pending[0].Title != "Delete plan.txt" {
		t.Fatalf("pending = %+v, want the one waiting chat", pending)
	}

	// Another member, or the same member in another organization, sees none of it.
	for _, other := range []Member{{UserID: "user_ravi", OrganizationID: "org_1"}, {UserID: asha.UserID, OrganizationID: "org_2"}} {
		if theirs, err := h.service.PendingConfirmations(ctx, other); err != nil || len(theirs) != 0 {
			t.Fatalf("%+v sees %+v, %v", other, theirs, err)
		}
	}

	h.run(asha, waiting.ID, RunInput{Confirmations: []Answer{{ID: confirmations[0].CallID, Confirmed: true}}})
	pending, err = h.service.PendingConfirmations(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after the answer = %+v", pending)
	}
}

func TestPendingConfirmationsReadsAThreadWithoutTheCountOnce(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hi.", genai.RoleModel), nil
	})
	ctx := context.Background()
	// A thread whose last run ended before runs kept the count.
	created, err := h.sessions.Create(ctx, &session.CreateRequest{
		AppName: "krude", UserID: asha.UserID,
		State: map[string]any{adksession.StateOrganizationID: asha.OrganizationID, stateOrigin: OriginChat},
	})
	if err != nil {
		t.Fatal(err)
	}
	asked := session.NewEvent(ctx, "old-run")
	asked.Author = "krude"
	asked.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
		ID: "confirm_1", Name: toolconfirmation.FunctionCallName,
	}}}}
	if err := h.sessions.AppendEvent(ctx, created.Session, asked); err != nil {
		t.Fatal(err)
	}

	pending, err := h.service.PendingConfirmations(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != created.Session.ID() || pending[0].PendingConfirmations != 1 {
		t.Fatalf("pending = %+v, want the old thread", pending)
	}
	key := legacyKey{app: "krude", user: asha.UserID, thread: created.Session.ID()}
	if count, ok := h.service.legacyCounts.get(key, pending[0].UpdatedAt); !ok || count != 1 {
		t.Fatalf("remembered = %d, %v; want the count kept until the thread changes", count, ok)
	}
	if _, ok := h.service.legacyCounts.get(key, pending[0].UpdatedAt.Add(1)); ok {
		t.Fatal("a changed thread reused its old count")
	}
}

func TestConfirmationTallyFollowsWhoAsksAndWhoAnswers(t *testing.T) {
	ask := func(author, id string) *session.Event {
		return &session.Event{Author: author, LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: id, Name: toolconfirmation.FunctionCallName},
		}}}}}
	}
	answer := func(author, id string) *session.Event {
		return &session.Event{Author: author, LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{ID: id, Name: toolconfirmation.FunctionCallName},
		}}}}}
	}
	partial := ask("krude", "c4")
	partial.Partial = true
	tally := tallyConfirmations([]*session.Event{
		ask("krude", "c1"), ask("krude", "c2"), ask("someone_else", "c3"), partial,
		answer(adkUserAuthor, "c1"), answer("krude", "c2"),
	}, "krude")
	// c2 was "answered" by the agent itself, which is no answer; c3 was asked
	// by an agent that does not run the thread; c4 is still streaming.
	if tally.pending() != 1 {
		t.Fatalf("pending = %d, want 1 (c2)", tally.pending())
	}
}
