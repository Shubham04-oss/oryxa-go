package threads

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// What a run records as it starts and ends is written to ADK's PostgreSQL
// service around the run's own events, and the next run on the thread is not
// refused as stale for it.
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
	var streamed []Item
	for _, message := range []string{"Hi", "Again"} {
		items, _ := h.run(member, created.ID, RunInput{Message: message})
		if got := items[len(items)-1]; got.Type != ItemRunCompleted {
			t.Fatalf("run %q ended with %+v", message, got)
		}
		streamed = append(streamed, find(items, ItemRunStarted)[0], items[len(items)-1])
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
	if got := strings.Join(types(detail.Items), ","); got != "run_started,message,message,run_completed,run_started,message,message,run_completed" {
		t.Fatalf("history = %s, want each run bracketed and nothing else of the record", got)
	}
	// Each bracket reads back as it was streamed: its id, and its time to the
	// microsecond the database keeps.
	var kept []Item
	for _, item := range detail.Items {
		if item.RunID != "" {
			kept = append(kept, item)
		}
	}
	for index, live := range streamed {
		if kept[index].ID != live.ID || kept[index].Type != live.Type || !kept[index].At.Equal(live.At) {
			t.Fatalf("bracket %d: stored %+v, streamed %+v", index, kept[index], live)
		}
	}
}
