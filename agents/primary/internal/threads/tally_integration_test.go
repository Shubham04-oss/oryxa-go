package threads

import (
	"context"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// What a run records as it ends is written to ADK's PostgreSQL service after
// the run's own events, and the next run on the thread is not refused as
// stale for it.
func TestARunsRecordSurvivesTheDatabase(t *testing.T) {
	h, member := databaseHarness(t)
	h.model.respond = func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hello.", genai.RoleModel), nil
	}
	ctx := context.Background()
	created, err := h.service.Create(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"Hi", "Again"} {
		items, _ := h.run(member, created.ID, RunInput{Message: message})
		if got := items[len(items)-1]; got.Type != ItemRunCompleted {
			t.Fatalf("run %q ended with %+v", message, got)
		}
	}
	stored, err := h.service.thread(ctx, member, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := stateInt(stored.State(), stateArtifactCount); !ok || count != 0 {
		t.Fatalf("recorded count = %d, %v; want 0, recorded", count, ok)
	}
	detail, err := h.service.Get(ctx, member, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(find(detail.Items, ItemMessage)) != 4 {
		t.Fatalf("history = %+v, want two messages each way and nothing of the record", detail.Items)
	}
}
