package subagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// openTestStore connects to the PostgreSQL AGENTS_PRIMARY_TEST_DATABASE_URL
// names and migrates the table into it, twice, as replicas starting
// together would. The pool has no schema set: every query names its own.
func openTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-subagents-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store := NewPostgresStore(database)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return store
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

// TestPostgresStoreRoundTrip: a record reads back as written, only through
// its own thread, member and organization; updates hold to the state they
// expect; progress touches only a running one; and deleting removes it.
func TestPostgresStoreRoundTrip(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	thread := Thread{OrganizationID: "org_" + suffix, UserID: "member_" + suffix, App: "krude", ID: "thread_" + suffix}
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := Record{
		ID: "sa_test_" + suffix, Thread: thread, ParentRunID: "run_1",
		Objective: "Find the price.", Output: "One line.", Tools: []string{ToolKnowledge, ToolWebSearch}, Files: []string{"prices.csv"},
		State: StateRunning, Progress: "starting", BudgetModelCalls: 30, BudgetMinutes: 5,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), record.ID) })
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, thread, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Objective != record.Objective || !slices.Equal(got.Tools, record.Tools) || !slices.Equal(got.Files, record.Files) ||
		got.State != StateRunning || !got.CreatedAt.Equal(now) || !got.ExpiresAt.Equal(record.ExpiresAt) || got.Thread != thread {
		t.Fatalf("read back %+v", got)
	}
	for _, other := range []Thread{
		{OrganizationID: "org_other", UserID: thread.UserID, App: thread.App, ID: thread.ID},
		{OrganizationID: thread.OrganizationID, UserID: "member_other", App: thread.App, ID: thread.ID},
		{OrganizationID: thread.OrganizationID, UserID: thread.UserID, App: "agent_other", ID: thread.ID},
		{OrganizationID: thread.OrganizationID, UserID: thread.UserID, App: thread.App, ID: "thread_other"},
	} {
		if _, err := store.Get(ctx, other, record.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("read through %+v: %v", other, err)
		}
	}

	if err := store.SetProgress(ctx, record.ID, "3 model calls so far; last used knowledge_search", 3, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, thread, record.ID); got.Progress != "3 model calls so far; last used knowledge_search" || got.ModelCalls != 3 {
		t.Fatalf("after progress: %+v", got)
	}

	done := got
	done.State, done.Result, done.FullResult, done.Progress = StateDone, "It costs $4.", "the whole of it", ""
	done.InputTokens, done.OutputTokens, done.CachedTokens, done.TotalTokens = 100, 20, 5, 120
	done.UpdatedAt = now.Add(time.Minute)
	if err := store.Update(ctx, done, StateWaiting); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("update from the wrong state = %v", err)
	}
	if err := store.Update(ctx, done, StateRunning); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, thread, record.ID); got.State != StateDone || got.Result != "It costs $4." || got.FullResult != "the whole of it" || got.TotalTokens != 120 || got.CachedTokens != 5 {
		t.Fatalf("after update: %+v", got)
	}
	if err := store.SetProgress(ctx, record.ID, "late", 9, now); err != nil {
		t.Fatal(err)
	}
	if got, _ = store.Get(ctx, thread, record.ID); got.Progress != "" || got.ModelCalls != 3 {
		t.Fatalf("progress on a done sub-agent changed it: %+v", got)
	}

	second := record
	second.ID, second.CreatedAt = "sa_test2_"+suffix, now.Add(time.Second)
	t.Cleanup(func() { _ = store.Delete(context.Background(), second.ID) })
	if err := store.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx, thread)
	if err != nil || len(listed) != 2 || listed[0].ID != record.ID || listed[1].ID != second.ID {
		t.Fatalf("list = %v, %v", listed, err)
	}
	member, err := store.ListMember(ctx, thread.OrganizationID, thread.UserID)
	if err != nil || len(member) != 2 {
		t.Fatalf("member list = %d, %v", len(member), err)
	}
	if everyone, err := store.ListMember(ctx, thread.OrganizationID, ""); err != nil || len(everyone) != 2 {
		t.Fatalf("organization list = %d, %v", len(everyone), err)
	}
	if err := store.Delete(ctx, record.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if listed, _ := store.List(ctx, thread); len(listed) != 0 {
		t.Fatalf("after delete: %v", listed)
	}
}

// TestPostgresStoreRefusesAnUnknownState: the table holds only the
// lifecycle's states.
func TestPostgresStoreRefusesAnUnknownState(t *testing.T) {
	store := openTestStore(t)
	suffix := randomSuffix(t)
	now := time.Now()
	record := Record{
		ID: "sa_test_" + suffix, Thread: Thread{OrganizationID: "org", UserID: "u", App: "krude", ID: "t"}, ParentRunID: "run",
		Objective: "x", State: StateExpired, BudgetModelCalls: 1, BudgetMinutes: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now,
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), record.ID) })
	if err := store.Create(context.Background(), record); err == nil {
		t.Fatal("an expired state was stored")
	}
}

// TestManagerOnPostgres: a sub-agent spawned, answered and reopened with
// its records and its transcript in PostgreSQL, ADK's session tables among
// them, as the service keeps them.
func TestManagerOnPostgres(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	sessionStore, err := adksession.Open(ctx, os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessionStore.Close)
	m := newTestManager(t, askingModel, func(config *Config) {
		config.Store = store
		config.Sessions = sessionStore.Sessions()
	})
	m.sessions = sessionStore.Sessions()
	tool, _ := m.run(t, ctx, "run_"+randomSuffix(t))
	tool.thread = "thread_" + randomSuffix(t)
	thread := Thread{OrganizationID: "org_1", UserID: "member_1", App: "krude", ID: tool.thread}
	t.Cleanup(func() {
		_ = m.ForgetThread(context.Background(), thread.OrganizationID, thread.UserID, thread.App, thread.ID)
	})

	view, err := m.spawn(tool, testBrief(true, ToolKnowledge))
	if err != nil || view.State != StateWaiting {
		t.Fatalf("spawn = %+v, %v", view, err)
	}
	answered, err := m.message(tool, messageArgs{ID: view.ID, Message: "euros", Wait: true})
	if err != nil || answered.State != StateDone || answered.Result != "Using euros: $4." {
		t.Fatalf("answered = %+v, %v", answered, err)
	}
	stored, err := store.Get(ctx, thread, view.ID)
	if err != nil || stored.ModelCalls != 2 || stored.TotalTokens != 30 || stored.State != StateDone {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	transcript, err := m.sessions.Get(ctx, &session.GetRequest{AppName: AppName, UserID: "member_1", SessionID: thread.ID + "/" + view.ID})
	if err != nil || transcript.Session.Events().Len() < 4 {
		t.Fatalf("transcript = %v, %v", transcript, err)
	}
	if err := m.ForgetThread(ctx, thread.OrganizationID, thread.UserID, thread.App, thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, thread, view.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after forgetting: %v", err)
	}
}
