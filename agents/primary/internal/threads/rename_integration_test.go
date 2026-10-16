package threads

import (
	"context"
	"testing"
)

// A rename is a state change ADK's PostgreSQL service keeps: the title reads
// back from the database, the history shows nothing of it, and a second
// rename right after is not refused as stale.
func TestARenameSurvivesTheDatabase(t *testing.T) {
	h, member := databaseHarness(t)
	ctx := context.Background()
	created, err := h.service.Create(ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Launch plan", "Launch plan, v2"} {
		renamed, err := h.service.Rename(ctx, member, created.ID, title)
		if err != nil {
			t.Fatalf("Rename(%q) error = %v", title, err)
		}
		if renamed.Title != title {
			t.Fatalf("renamed = %+v, want %q", renamed, title)
		}
	}
	detail, err := h.service.Get(ctx, member, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != "Launch plan, v2" || len(detail.Items) != 0 {
		t.Fatalf("detail = %+v, want the last title and no history items", detail)
	}
}
