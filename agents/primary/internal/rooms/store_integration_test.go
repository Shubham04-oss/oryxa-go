package rooms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The store's tests need the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa@127.0.0.1:55441/oryxa?sslmode=disable' \
//	  go test ./agents/primary/internal/rooms
//
// Each test works in an organization, and a room within it, of its own.
type fixture struct {
	store          *PostgresStore
	teamspaces     *teamspaces.Store
	database       *postgreskit.Database
	organizationID string
}

func testStore(t *testing.T) *fixture {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-rooms-test"})
	if err != nil {
		t.Fatal(err)
	}
	// A room is a teamspace, so its tables lean on teamspaces' own — which in
	// turn needs runstore and managedagents applied first (teamspaces'
	// migration foreign-keys contributions to member_agents). This is the
	// same dependency order every other agents/primary package migrates in.
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := managedagents.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	teamspaceStore := teamspaces.NewStore(database)
	if err := teamspaceStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	organizationID := "rooms_test_" + hex.EncodeToString(random)
	fixture := &fixture{store: store, teamspaces: teamspaceStore, database: database, organizationID: organizationID}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_messages WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_briefs WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_presence WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_agent_threads WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_message_seqs WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		database.Close()
	})
	return fixture
}

// newRoom creates a fresh teamspace and returns its id: a room, exactly as
// rooms.go declares one to be.
func (fixture *fixture) newRoom(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	suffix := hex.EncodeToString(random)
	organizationAgent, err := fixture.teamspaces.EnsureOrganizationAgent(ctx, fixture.organizationID, "Rooms Test", "user_owner_"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	space, err := fixture.teamspaces.Create(ctx, teamspaces.Space{
		ID: "room_" + suffix, OrganizationID: fixture.organizationID, TeamID: "team_" + suffix,
		Name: "Room " + suffix, CreatedByUserID: "user_owner_" + suffix, OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return space.ID
}

func TestAppendAllocatesAGaplessSequenceUnderConcurrentPosts(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	const n = 25
	var group sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			message, err := fixture.store.Append(ctx, Message{
				ID: fmt.Sprintf("msg_%s_%d", roomID, i), RoomID: roomID,
				AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada",
				Body: fmt.Sprintf("message %d", i),
			})
			seqs[i], errs[i] = message.Seq, err
		}(i)
	}
	group.Wait()

	seen := map[int64]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Append(%d) error = %v", i, err)
		}
		if seen[seqs[i]] {
			t.Fatalf("seq %d was given out twice; all seqs=%v", seqs[i], seqs)
		}
		seen[seqs[i]] = true
	}
	for want := int64(1); want <= n; want++ {
		if !seen[want] {
			t.Fatalf("seq %d was never given out; got %v, want exactly 1..%d", want, seqs, n)
		}
	}
}

func TestMessagesAndRecentOrderingAndLimits(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	const total = 5
	var appended []Message
	for i := range total {
		message, err := fixture.store.Append(ctx, Message{
			ID: fmt.Sprintf("msg_%s_%d", roomID, i), RoomID: roomID,
			AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada",
			Body: fmt.Sprintf("message %d", i), AddressedTo: []string{"bea"},
		})
		if err != nil {
			t.Fatal(err)
		}
		appended = append(appended, message)
	}
	if !slices.Equal(appended[0].AddressedTo, []string{"bea"}) {
		t.Fatalf("AddressedTo round-tripped as %v, want [bea]", appended[0].AddressedTo)
	}

	all, err := fixture.store.Messages(ctx, roomID, 0, 100)
	if err != nil || len(all) != total {
		t.Fatalf("Messages(afterSeq=0) = %d messages, %v, want %d", len(all), err, total)
	}
	for i, message := range all {
		if message.Seq != int64(i+1) {
			t.Fatalf("Messages()[%d].Seq = %d, want %d (oldest first)", i, message.Seq, i+1)
		}
	}

	after, err := fixture.store.Messages(ctx, roomID, 2, 100)
	if err != nil || len(after) != total-2 || after[0].Seq != 3 {
		t.Fatalf("Messages(afterSeq=2) = %+v, %v, want seqs 3..%d", after, err, total)
	}

	limited, err := fixture.store.Messages(ctx, roomID, 0, 2)
	if err != nil || len(limited) != 2 || limited[0].Seq != 1 || limited[1].Seq != 2 {
		t.Fatalf("Messages(limit=2) = %+v, %v, want seqs 1,2", limited, err)
	}

	recent, err := fixture.store.Recent(ctx, roomID, 2)
	if err != nil || len(recent) != 2 || recent[0].Seq != total-1 || recent[1].Seq != total {
		t.Fatalf("Recent(2) = %+v, %v, want the last two, oldest first", recent, err)
	}
}

func TestSaveBriefCompareAndSetLosesSafely(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	zero, err := fixture.store.Brief(ctx, roomID)
	if err != nil || zero.Revision != 0 || zero.RoomID != roomID {
		t.Fatalf("Brief() before any save = %+v, %v, want the zero brief", zero, err)
	}

	// Every racer reads the same "no brief yet" state and tries to plant the
	// first revision with the same expected=0. Exactly one may actually
	// write; the rest must see it lost and hand back what the winner wrote,
	// not an error and not a second revision 1.
	const attempts = 8
	var group sync.WaitGroup
	revisions := make([]int64, attempts)
	saveErrs := make([]error, attempts)
	for i := range attempts {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			saved, err := fixture.store.SaveBrief(ctx, Brief{
				RoomID: roomID, ThroughSeq: 1, Summary: fmt.Sprintf("summary %d", i),
				Decided: []string{"decided"}, Open: []string{"open"}, Owed: []string{"owed"},
			}, 0)
			revisions[i], saveErrs[i] = saved.Revision, err
		}(i)
	}
	group.Wait()

	for i, err := range saveErrs {
		if err != nil {
			t.Fatalf("SaveBrief(%d) with expected=0 error = %v", i, err)
		}
		if revisions[i] != 1 {
			t.Fatalf("SaveBrief(%d) with expected=0 returned revision %d, want every racer to converge on 1", i, revisions[i])
		}
	}

	current, err := fixture.store.Brief(ctx, roomID)
	if err != nil || current.Revision != 1 {
		t.Fatalf("Brief() after the race = %+v, %v, want revision 1", current, err)
	}
	matched := false
	for i := range attempts {
		if current.Summary == fmt.Sprintf("summary %d", i) {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("stored brief summary %q matches none of the racing attempts", current.Summary)
	}

	// A stale expected value fails cleanly and leaves the stored brief alone.
	stale, err := fixture.store.SaveBrief(ctx, Brief{RoomID: roomID, Summary: "stale"}, 0)
	if err != nil || stale.Revision != 1 || stale.Summary == "stale" {
		t.Fatalf("SaveBrief(stale expected=0) = %+v, %v, want the current brief unchanged, no error", stale, err)
	}

	next, err := fixture.store.SaveBrief(ctx, Brief{
		RoomID: roomID, ThroughSeq: 2, Summary: "second", Decided: []string{"d2"},
	}, current.Revision)
	if err != nil || next.Revision != 2 || next.Summary != "second" || len(next.Open) != 0 || len(next.Owed) != 0 {
		t.Fatalf("SaveBrief(current revision) = %+v, %v, want revision 2 with empty open/owed", next, err)
	}
}

func TestWatchingListsWhoReadSinceACutoff(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	if watchers, err := fixture.store.Watching(ctx, roomID, time.Unix(0, 0)); err != nil || len(watchers) != 0 {
		t.Fatalf("Watching() before any Watch = %v, %v, want none", watchers, err)
	}

	early := time.Now().UTC().Add(-time.Hour)
	late := time.Now().UTC()
	if err := fixture.store.Watch(ctx, roomID, "user_ada", early); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Watch(ctx, roomID, "user_bea", late); err != nil {
		t.Fatal(err)
	}

	sinceEarly, err := fixture.store.Watching(ctx, roomID, early)
	if err != nil || !slices.Equal(sinceEarly, []string{"user_ada", "user_bea"}) {
		t.Fatalf("Watching(since=early) = %v, %v, want both watchers", sinceEarly, err)
	}
	sinceLate, err := fixture.store.Watching(ctx, roomID, late)
	if err != nil || !slices.Equal(sinceLate, []string{"user_bea"}) {
		t.Fatalf("Watching(since=late) = %v, %v, want only user_bea", sinceLate, err)
	}

	// A delayed, older ping cannot move last_read_at backward.
	if err := fixture.store.Watch(ctx, roomID, "user_bea", early); err != nil {
		t.Fatal(err)
	}
	stillLate, err := fixture.store.Watching(ctx, roomID, late)
	if err != nil || !slices.Equal(stillLate, []string{"user_bea"}) {
		t.Fatalf("Watching(since=late) after a stale ping = %v, %v, want user_bea to stay caught up", stillLate, err)
	}
}

func TestThreadIsRememberedAndCleared(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	if threadID, err := fixture.store.Thread(ctx, roomID, "agent_a"); err != nil || threadID != "" {
		t.Fatalf("Thread() before any SetThread = %q, %v, want empty", threadID, err)
	}
	for _, threadID := range []string{"thread_1", "thread_2"} {
		if err := fixture.store.SetThread(ctx, roomID, "agent_a", threadID); err != nil {
			t.Fatal(err)
		}
	}
	if threadID, err := fixture.store.Thread(ctx, roomID, "agent_a"); err != nil || threadID != "thread_2" {
		t.Fatalf("Thread() = %q, %v, want the latest write", threadID, err)
	}
	// Clearing says the old thread is gone; a caller takes a fresh one next.
	if err := fixture.store.SetThread(ctx, roomID, "agent_a", ""); err != nil {
		t.Fatal(err)
	}
	if threadID, err := fixture.store.Thread(ctx, roomID, "agent_a"); err != nil || threadID != "" {
		t.Fatalf("Thread() after clearing = %q, %v, want empty again", threadID, err)
	}
}

func TestOperationsOnARoomWithNoTeamspaceFailCleanly(t *testing.T) {
	fixture := testStore(t)
	ctx := context.Background()
	missingRoomID := "room_never_created_" + fixture.organizationID

	if _, err := fixture.store.Append(ctx, Message{
		ID: "msg_missing", RoomID: missingRoomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada", Body: "hi",
	}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("Append() into a room with no teamspace = %v, want ErrRoomNotFound", err)
	}
	if _, err := fixture.store.SaveBrief(ctx, Brief{RoomID: missingRoomID, Summary: "x"}, 0); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("SaveBrief() into a room with no teamspace = %v, want ErrRoomNotFound", err)
	}
	if err := fixture.store.Watch(ctx, missingRoomID, "user_ada", time.Now().UTC()); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("Watch() into a room with no teamspace = %v, want ErrRoomNotFound", err)
	}
	if err := fixture.store.SetThread(ctx, missingRoomID, "agent_a", "thread_1"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("SetThread() into a room with no teamspace = %v, want ErrRoomNotFound", err)
	}
}

func TestForgetRemovesAMembersOrTheOrganizationsRoomData(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()

	adaAgent, err := managedagents.NewStore(fixture.database).Create(ctx,
		managedagents.Scope{OrganizationID: fixture.organizationID, UserID: "user_ada"},
		managedagents.Definition{Name: "Ada's Agent", Description: "test agent", Instructions: "Reply."})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.Append(ctx, Message{
		ID: "msg_forget_ada", RoomID: roomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada", Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Append(ctx, Message{
		ID: "msg_forget_bea", RoomID: roomID, AuthorHandle: "bea", AuthorKind: SeatPerson, AuthorUserID: "user_bea", Body: "hi back",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Watch(ctx, roomID, "user_ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Watch(ctx, roomID, "user_bea", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SetThread(ctx, roomID, adaAgent.ID, "thread_ada"); err != nil {
		t.Fatal(err)
	}
	brief, err := fixture.store.SaveBrief(ctx, Brief{RoomID: roomID, Summary: "shared"}, 0)
	if err != nil || brief.Revision != 1 {
		t.Fatalf("SaveBrief() = %+v, %v", brief, err)
	}

	if err := fixture.store.Forget(ctx, fixture.organizationID, "user_ada"); err != nil {
		t.Fatal(err)
	}
	remaining, err := fixture.store.Messages(ctx, roomID, 0, 100)
	if err != nil || len(remaining) != 1 || remaining[0].AuthorUserID != "user_bea" {
		t.Fatalf("after forgetting user_ada, messages = %+v, %v, want only user_bea's", remaining, err)
	}
	watchers, err := fixture.store.Watching(ctx, roomID, time.Now().UTC().Add(-time.Hour))
	if err != nil || !slices.Equal(watchers, []string{"user_bea"}) {
		t.Fatalf("after forgetting user_ada, watchers = %v, %v, want only user_bea", watchers, err)
	}
	if threadID, err := fixture.store.Thread(ctx, roomID, adaAgent.ID); err != nil || threadID != "" {
		t.Fatalf("forgetting user_ada should clear their agent's room thread: %q, %v", threadID, err)
	}
	if stillThere, err := fixture.store.Brief(ctx, roomID); err != nil || stillThere.Revision != 1 {
		t.Fatalf("forgetting a member should leave the room's own brief alone: %+v, %v", stillThere, err)
	}

	if err := fixture.store.Forget(ctx, fixture.organizationID, ""); err != nil {
		t.Fatal(err)
	}
	if remaining, err := fixture.store.Messages(ctx, roomID, 0, 100); err != nil || len(remaining) != 0 {
		t.Fatalf("after forgetting the organization, messages = %+v, %v, want none", remaining, err)
	}
	if watchers, err := fixture.store.Watching(ctx, roomID, time.Now().UTC().Add(-time.Hour)); err != nil || len(watchers) != 0 {
		t.Fatalf("after forgetting the organization, watchers = %v, %v, want none", watchers, err)
	}
	if zero, err := fixture.store.Brief(ctx, roomID); err != nil || zero.Revision != 0 {
		t.Fatalf("after forgetting the organization, brief = %+v, %v, want it wiped too", zero, err)
	}
	// The counter is gone too, so this room starts over at seq 1 rather than
	// silently continuing where the forgotten organization left off.
	fresh, err := fixture.store.Append(ctx, Message{
		ID: "msg_after_forget", RoomID: roomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada", Body: "again",
	})
	if err != nil || fresh.Seq != 1 {
		t.Fatalf("Append() after organization forget = %+v, %v, want seq to restart at 1", fresh, err)
	}
}
