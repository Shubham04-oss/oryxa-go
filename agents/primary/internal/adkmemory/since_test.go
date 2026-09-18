package adkmemory

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
)

// spokenIn returns the text of every event in a conversation.
func spokenIn(conversation session.Session) []string {
	var said []string
	for event := range conversation.Events().All() {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.Text != "" {
				said = append(said, part.Text)
			}
		}
	}
	return said
}

func TestSinceKeepsOnlyWhatCameAfterTheMark(t *testing.T) {
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: appName, UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	add := func(text string, at time.Time) {
		t.Helper()
		event := session.NewEvent(ctx, "invocation")
		event.Author = "user"
		event.Content = genai.NewContentFromText(text, genai.RoleUser)
		event.Timestamp = at
		if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	mark := time.Now()
	add("an earlier turn", mark.Add(-time.Minute))
	add("the turn the mark sits on", mark)
	add("this run's turn", mark.Add(time.Minute))

	kept := Since(created.Session, mark)
	if got := spokenIn(kept); len(got) != 1 || got[0] != "this run's turn" {
		t.Fatalf("kept %q, want only what came after the mark", got)
	}
	if kept.Events().Len() != 1 {
		t.Fatalf("Len() = %d, want 1", kept.Events().Len())
	}
	if at := kept.Events().At(0); at == nil || at.Content.Parts[0].Text != "this run's turn" {
		t.Fatalf("At(0) = %+v", at)
	}
	if kept.Events().At(1) != nil || kept.Events().At(-1) != nil {
		t.Fatal("At() outside the kept events returned something")
	}

	// The view is the conversation in every other respect, so the scoped
	// service's check that it belongs to the member still holds.
	if kept.UserID() != created.Session.UserID() || kept.ID() != created.Session.ID() {
		t.Fatalf("the view changed whose conversation it is: %s/%s", kept.UserID(), kept.ID())
	}
	if got := spokenIn(created.Session); len(got) != 3 {
		t.Fatalf("the conversation itself was changed: %q", got)
	}
}

func TestSinceWithNoMarkKeepsEverything(t *testing.T) {
	stored := conversation(t, "user-1", "one", "two")
	if got := spokenIn(Since(stored, time.Time{})); len(got) != 2 {
		t.Fatalf("kept %q, want everything", got)
	}
	if Since(nil, time.Now()) != nil {
		t.Fatal("Since(nil) returned a conversation")
	}
}
