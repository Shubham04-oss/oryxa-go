package adksession

import (
	"context"
	"os"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
)

func TestOpenRequiresDatabaseURL(t *testing.T) {
	if _, err := Open(context.Background(), "", true); err == nil {
		t.Fatal("Open() error = nil")
	}
}

func TestPostgresSessionsSurviveReconnect(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	first, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.Sessions().Create(ctx, &session.CreateRequest{
		AppName: "krude-test", UserID: t.Name(), State: map[string]any{"marker": "persisted"},
	})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	event := session.NewEvent(ctx, "test-invocation")
	event.Author = "krude"
	event.Content = genai.NewContentFromText("persist this event", genai.RoleModel)
	event.Actions.StateDelta = map[string]any{"event_marker": "persisted"}
	if err := first.Sessions().AppendEvent(ctx, created.Session, event); err != nil {
		first.Close()
		t.Fatal(err)
	}
	first.Close()

	second, err := Open(ctx, databaseURL, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = second.Sessions().Delete(ctx, &session.DeleteRequest{
			AppName: "krude-test", UserID: t.Name(), SessionID: created.Session.ID(),
		})
		second.Close()
	}()
	loaded, err := second.Sessions().Get(ctx, &session.GetRequest{
		AppName: "krude-test", UserID: t.Name(), SessionID: created.Session.ID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	marker, err := loaded.Session.State().Get("marker")
	if err != nil || marker != "persisted" {
		t.Fatalf("persisted marker = %v, error = %v", marker, err)
	}
	eventMarker, err := loaded.Session.State().Get("event_marker")
	if err != nil || eventMarker != "persisted" {
		t.Fatalf("event marker = %v, error = %v", eventMarker, err)
	}
	if loaded.Session.Events().Len() != 1 || loaded.Session.Events().At(0).Author != "krude" {
		t.Fatalf("persisted events = %d, want one Krude event", loaded.Session.Events().Len())
	}
}
