package adksession

import (
	"context"
	"errors"
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

// ADK's temporary state, the "temp:" prefix, belongs to one invocation: a tool
// reads it while the run goes on, and it must reach neither the session's
// stored state nor the actions of a stored event. A member's screen snapshot is
// kept there; anywhere else it would stay for as long as the thread lives.
func TestPostgresTemporaryStateIsNeverStored(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, databaseURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := store.Sessions()
	created, err := service.Create(ctx, &session.CreateRequest{AppName: "krude-temp-state-test", UserID: t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := created.Session.ID()
	defer func() {
		if err := service.Delete(ctx, &session.DeleteRequest{AppName: "krude-temp-state-test", UserID: t.Name(), SessionID: sessionID}); err != nil {
			t.Errorf("cleanup session: %v", err)
		}
	}()

	event := session.NewEvent(ctx, "temp-state-invocation")
	event.Author = "user"
	event.Content = genai.NewContentFromText("what is this?", genai.RoleUser)
	event.Actions.StateDelta = map[string]any{"temp:snapshot": "one run only", "kept": "for the thread"}
	if err := service.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}

	// The invocation goes on with the session it already holds, temporary
	// state included.
	if value, err := created.Session.State().Get("temp:snapshot"); err != nil || value != "one run only" {
		t.Fatalf("temporary state during the invocation = %v, %v; want it readable", value, err)
	}

	// The next run reads the session back from the database.
	loaded, err := service.Get(ctx, &session.GetRequest{AppName: "krude-temp-state-test", UserID: t.Name(), SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := loaded.Session.State().Get("temp:snapshot"); !errors.Is(err, session.ErrStateKeyNotExist) {
		t.Fatalf("temporary state after reload = %v, %v; want it gone", value, err)
	}
	if value, err := loaded.Session.State().Get("kept"); err != nil || value != "for the thread" {
		t.Fatalf("ordinary state after reload = %v, %v; want it kept", value, err)
	}
	for stored := range loaded.Session.Events().All() {
		if _, found := stored.Actions.StateDelta["temp:snapshot"]; found {
			t.Fatalf("event %s came back with the temporary state in its actions", stored.ID)
		}
	}

	// And the tables hold no trace of it, though they do hold what was kept.
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := store.Database().SQLX.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const eventsWith = `SELECT count(*) FROM agents_primary.events WHERE session_id = $1 AND position($2::bytea in actions) > 0`
	const stateWith = `SELECT count(*) FROM agents_primary.sessions WHERE id = $1 AND state::text LIKE $2`
	if got := count(eventsWith, sessionID, []byte("kept")); got != 1 {
		t.Fatalf("events whose actions hold the ordinary key = %d, want 1 (the check itself is broken)", got)
	}
	if got := count(stateWith, sessionID, "%kept%"); got != 1 {
		t.Fatalf("sessions whose state holds the ordinary key = %d, want 1 (the check itself is broken)", got)
	}
	if got := count(eventsWith, sessionID, []byte("temp:snapshot")); got != 0 {
		t.Fatalf("%d stored events carry the temporary key in their actions", got)
	}
	if got := count(stateWith, sessionID, "%temp:snapshot%"); got != 0 {
		t.Fatalf("the stored session state carries the temporary key")
	}
}
