package threads

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	adkdatabase "google.golang.org/adk/v2/session/database"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// notesTestUserPrefix marks every session these tests make, so a run that died
// halfway is cleaned up by the next.
const notesTestUserPrefix = "user_notes_it_"

// databaseHarness is the threads service over ADK's own PostgreSQL-backed
// session service, as production runs it: what a note is stored as, what comes
// back, and what the database says of two writers to one thread are the
// database's, and not the in-memory service's.
func databaseHarness(t *testing.T) (*harness, Member) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-threads-notes-test", Schema: "agents_primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := adksession.Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database.SQLX.DB}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := adkdatabase.NewSessionServiceFromDB(gormDB)
	if err != nil {
		t.Fatal(err)
	}
	clean := func() {
		for _, table := range []string{"events", "sessions", "user_states"} {
			_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.`+table+` WHERE user_id LIKE $1`, notesTestUserPrefix+"%")
		}
	}
	clean()
	t.Cleanup(clean)

	h := newHarness(t, nil)
	h.service.sessions = sessions
	return h, Member{UserID: fmt.Sprintf("%s%d", notesTestUserPrefix, time.Now().UnixNano()), OrganizationID: "org_notes_it"}
}

func TestANoteSurvivesTheDatabaseAndReadsBackAsARequestItem(t *testing.T) {
	h, member := databaseHarness(t)
	ctx := context.Background()
	note := Note{RequestID: "irq_1", State: NoteAnswered, Text: "Asked Raya's agent in #launch · answered"}
	if err := h.service.Note(ctx, member, note); err != nil {
		t.Fatal(err)
	}
	// Written again, it is the same line.
	if err := h.service.Note(ctx, member, note); err != nil {
		t.Fatal(err)
	}
	detail, err := h.service.Get(ctx, member, RequestsThreadID(member.OrganizationID))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != RequestsTitle || detail.Origin != OriginChat {
		t.Fatalf("thread = %+v", detail.Thread)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("items = %+v, want the one line", detail.Items)
	}
	item := detail.Items[0]
	if item.Type != ItemRequest || item.RequestID != "irq_1" || item.Status != NoteAnswered || item.Text != note.Text ||
		item.Author != AuthorOryxa || item.ID == "" || item.At.IsZero() {
		t.Fatalf("item = %+v, want it as it was written, after a round trip through JSON", item)
	}
}

func TestNotesInOrderAndTheThreadIsListedAndDeletedLikeAnyOther(t *testing.T) {
	h, member := databaseHarness(t)
	ctx := context.Background()
	for i := range 3 {
		if err := h.service.Note(ctx, member, Note{RequestID: fmt.Sprintf("irq_%d", i), State: NoteAsked, Text: fmt.Sprintf("line %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	items := requestItems(t, h, member)
	if len(items) != 3 || items[0].Text != "line 0" || items[1].Text != "line 1" || items[2].Text != "line 2" {
		t.Fatalf("items = %+v, want them in the order they were written", items)
	}
	listed, err := h.service.List(ctx, member)
	if err != nil || len(listed) != 1 || listed[0].Title != RequestsTitle {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	if err := h.service.Delete(ctx, member, RequestsThreadID(member.OrganizationID)); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Note(ctx, member, Note{RequestID: "irq_9", State: NoteAsked, Text: "line 9"}); err != nil {
		t.Fatal(err)
	}
	if items := requestItems(t, h, member); len(items) != 1 || items[0].RequestID != "irq_9" {
		t.Fatalf("items = %+v, want a new thread with only what was written since", items)
	}
}

func TestNotesWrittenAtOnceToTheDatabaseAllLand(t *testing.T) {
	h, member := databaseHarness(t)
	ctx := context.Background()
	const writers = 8
	var group sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[i] = h.service.Note(ctx, member, Note{RequestID: fmt.Sprintf("irq_%d", i), State: NoteAsked, Text: fmt.Sprintf("line %d", i)})
		}()
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	items := requestItems(t, h, member)
	seen := map[string]int{}
	for _, item := range items {
		seen[item.RequestID]++
	}
	if len(items) != writers || len(seen) != writers {
		t.Fatalf("got %d items for %d requests, want %d each once: %v", len(items), len(seen), writers, seen)
	}
	listed, err := h.service.List(ctx, member)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %+v, %v, want the one thread however many writers made it", listed, err)
	}
}
