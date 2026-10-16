package rooms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
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
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_read_cursors WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.room_message_seqs WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
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

// roomEvent is one event the agents outbox holds about a room's messages.
type roomEvent struct {
	occurredAt time.Time
	detail     string
	posted     agentsv1.TeamspaceRoomMessagePosted
}

// postedEvents is what the agents outbox holds about roomID's messages, in
// the order they were posted.
func (fixture *fixture) postedEvents(t *testing.T, roomID string) []roomEvent {
	t.Helper()
	rows, err := fixture.database.Pool.Query(context.Background(), `SELECT occurred_at, detail::text
		FROM agents_primary.agents_outbox
		WHERE detail_type=$1 AND detail->>'teamspace_id'=$2
		ORDER BY (detail->'message'->>'seq')::bigint`, agentsv1.TeamspaceRoomMessagePostedDetailType, roomID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []roomEvent
	for rows.Next() {
		var event roomEvent
		if err := rows.Scan(&event.occurredAt, &event.detail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(event.detail), &event.posted); err != nil {
			t.Fatal(err)
		}
		found = append(found, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

// TestAppendAnnouncesEveryMessageItStores: a person's message and an agent's
// answer each commit with their event, whose fields are the stored
// message's, and none of what it said. Handles are as a room makes them, with
// no "@".
func TestAppendAnnouncesEveryMessageItStores(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()
	asked, err := fixture.store.Append(ctx, Message{
		RoomID: roomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada",
		Body: "@crew can you draft the launch note?", AddressedTo: []string{"crew"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Append(ctx, Message{
		RoomID: roomID, AuthorHandle: "crew", AuthorKind: SeatAgent, AuthorUserID: "user_ada", AuthorAgentID: "agt_crew",
		Body: "Drafted: the launch is on Tuesday.", InReplyToSeq: asked.Seq,
	}); err != nil {
		t.Fatal(err)
	}

	stored, err := fixture.store.Messages(ctx, roomID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	events := fixture.postedEvents(t, roomID)
	if len(stored) != 2 || len(events) != 2 {
		t.Fatalf("stored %d messages and %d events, want two of each", len(stored), len(events))
	}
	want := []agentsv1.TeamspaceRoomMessagePosted{
		{
			OrganizationID: fixture.organizationID, TeamspaceID: roomID,
			Message:          agentsv1.TeamspaceRoomMessage{ID: stored[0].ID, Seq: 1},
			Author:           agentsv1.TeamspaceRoomAuthor{Kind: agentsv1.TeamspaceRoomAuthorMember, UserID: "user_ada"},
			AddressedHandles: []string{"crew"}, PostedAt: stored[0].CreatedAt,
		},
		{
			OrganizationID: fixture.organizationID, TeamspaceID: roomID,
			Message:  agentsv1.TeamspaceRoomMessage{ID: stored[1].ID, Seq: 2, InReplyToSeq: 1},
			Author:   agentsv1.TeamspaceRoomAuthor{Kind: agentsv1.TeamspaceRoomAuthorAgent, UserID: "user_ada", AgentID: "agt_crew"},
			PostedAt: stored[1].CreatedAt,
		},
	}
	for index, event := range events {
		got, expected := event.posted, want[index]
		if got.OrganizationID != expected.OrganizationID || got.TeamspaceID != expected.TeamspaceID || got.Message != expected.Message ||
			got.Author != expected.Author || !slices.Equal(got.AddressedHandles, expected.AddressedHandles) ||
			!got.PostedAt.Equal(expected.PostedAt) || !event.occurredAt.Equal(stored[index].CreatedAt) {
			t.Errorf("event %d = %+v at %s, want %+v at the stored message's time", index, got, event.occurredAt, expected)
		}
		for _, said := range []string{"launch note", "Tuesday", "body"} {
			if strings.Contains(event.detail, said) {
				t.Errorf("event %d carries what was said: %s", index, event.detail)
			}
		}
	}
	// A message that named nobody says so by leaving the field out.
	if strings.Contains(events[1].detail, "addressed_handles") {
		t.Errorf("the answer's event names handles it did not: %s", events[1].detail)
	}
}

// TestARoomPublishesOnlyTheSeatsItsMessagesNamed posts through the relay onto
// the real store, as a room does: a person's "@seat" is stored with the
// message and published with it, and an email address in an agent's answer is
// neither, so nothing of what either said reaches the bus.
func TestARoomPublishesOnlyTheSeatsItsMessagesNamed(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()
	ada := Seat{Handle: "ada", Kind: SeatPerson, UserID: "user_ada", DisplayName: "Ada", Present: true}
	crew := Seat{Handle: "crew", Kind: SeatAgent, UserID: "user_ada", AgentID: "agt_crew", AgentName: "Crew", DisplayName: "Crew"}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "crew" {
			return "Drafted, and sent to ops@acme-merger-2027.com; @ada, it is in your inbox.", nil
		}
		return Pass, nil
	}}
	relay := &Relay{Store: fixture.store, Speaker: speaker}
	said, err := relay.Post(ctx, Situation{RoomID: roomID, OrganizationID: fixture.organizationID, Seats: []Seat{ada, crew}}, Message{
		RoomID: roomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada",
		Body: "@crew can you draft the launch note for billing@acme-merger-2027.com?",
	}, Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(said) != 2 || said[1].AuthorHandle != "crew" {
		t.Fatalf("the room said %+v, want Ada's message and Crew's answer", said)
	}

	rows, err := fixture.database.Pool.Query(ctx, `SELECT seq, addressed_to FROM agents_primary.room_messages
		WHERE room_id=$1 ORDER BY seq`, roomID)
	if err != nil {
		t.Fatal(err)
	}
	stored := map[int64][]string{}
	for rows.Next() {
		var seq int64
		var addressed []string
		if err := rows.Scan(&seq, &addressed); err != nil {
			t.Fatal(err)
		}
		stored[seq] = addressed
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	events := fixture.postedEvents(t, roomID)
	if len(stored) != 2 || len(events) != 2 {
		t.Fatalf("stored %v and events %+v, want two of each", stored, events)
	}
	for index, want := range [][]string{{"crew"}, {"ada"}} {
		seq := int64(index + 1)
		if !slices.Equal(stored[seq], want) {
			t.Errorf("message %d is stored addressing %v, want %v", seq, stored[seq], want)
		}
		if got := events[index].posted; got.Message.Seq != seq || !slices.Equal(got.AddressedHandles, want) {
			t.Errorf("message %d's event = %s, want it to name %v", seq, events[index].detail, want)
		}
		for _, word := range []string{"acme-merger", "ops@", "billing@", "inbox", "launch note"} {
			if strings.Contains(events[index].detail, word) {
				t.Errorf("message %d's event carries %q from what was said: %s", seq, word, events[index].detail)
			}
		}
	}
}

// TestAppendAnnouncesNothingItDidNotStore: a message that is not stored has
// no event, and an event that cannot be recorded takes its message with it,
// the place in the room included.
func TestAppendAnnouncesNothingItDidNotStore(t *testing.T) {
	fixture := testStore(t)
	roomID := fixture.newRoom(t)
	ctx := context.Background()
	message := func(id string) Message {
		return Message{ID: id, RoomID: roomID, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user_ada", Body: "Morning."}
	}

	if _, err := fixture.store.Append(ctx, Message{RoomID: "room_gone_" + fixture.organizationID, AuthorHandle: "ada",
		AuthorKind: SeatPerson, AuthorUserID: "user_ada", Body: "Anyone?"}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("Append() to a room that does not exist = %v", err)
	}
	if events := fixture.postedEvents(t, "room_gone_"+fixture.organizationID); len(events) != 0 {
		t.Fatalf("a message to no room has events %+v", events)
	}

	duplicate := "rmm_duplicate_" + fixture.organizationID
	if _, err := fixture.store.Append(ctx, message(duplicate)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Append(ctx, message(duplicate)); err == nil {
		t.Fatal("Append() of a message id already stored = nil error")
	}

	fixture.store.recordPosted = func(ctx context.Context, tx pgx.Tx, organizationID string, message Message) error {
		if err := recordPosted(ctx, tx, organizationID, message); err != nil {
			return err
		}
		return errors.New("the outbox refused the event")
	}
	if _, err := fixture.store.Append(ctx, message("rmm_unannounced_"+fixture.organizationID)); err == nil ||
		!strings.Contains(err.Error(), "the outbox refused the event") {
		t.Fatalf("Append() whose event failed = %v", err)
	}
	fixture.store.recordPosted = recordPosted

	stored, err := fixture.store.Messages(ctx, roomID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events := fixture.postedEvents(t, roomID); len(stored) != 1 || len(events) != 1 || events[0].posted.Message.ID != duplicate {
		t.Fatalf("stored %+v with events %+v; want the first message alone, and its one event", stored, events)
	}
	// Neither failure kept the place it was given.
	next, err := fixture.store.Append(ctx, message(""))
	if err != nil || next.Seq != 2 {
		t.Fatalf("the next message = %+v, %v; want it second in the room", next, err)
	}
	if events := fixture.postedEvents(t, roomID); len(events) != 2 || events[1].posted.Message.Seq != 2 {
		t.Fatalf("events = %+v, want the next message's too", events)
	}
}

// A read cursor moves only forward and stops at the room's last message, and
// activity reads every room's unread count and newest message at once,
// counting nothing the reader said themself.
func TestReadCursorsAndActivity(t *testing.T) {
	fixture := testStore(t)
	ctx := context.Background()
	busy, quiet := fixture.newRoom(t), fixture.newRoom(t)
	for _, message := range []Message{
		{RoomID: busy, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user-ada", Body: "morning"},
		{RoomID: busy, AuthorHandle: "bo", AuthorKind: SeatPerson, AuthorUserID: "user-bo", Body: "morning Ada"},
		{RoomID: busy, AuthorHandle: "mira", AuthorKind: SeatAgent, AuthorUserID: "user-ada", AuthorAgentID: "agt_ada", Body: "Ada's agent, standing in"},
	} {
		if _, err := fixture.store.Append(ctx, message); err != nil {
			t.Fatal(err)
		}
	}

	activity, err := fixture.store.Activity(ctx, fixture.organizationID, "user-ada", []string{busy, quiet, "room_missing"})
	if err != nil {
		t.Fatal(err)
	}
	if got := activity[busy]; got.LastReadSeq != 0 || got.Unread != 2 || got.Last == nil || got.Last.Seq != 3 || got.Last.AuthorAgentID != "agt_ada" {
		t.Fatalf("busy room = %+v (last %+v), want Bo's message and Ada's agent's unread", got, got.Last)
	}
	if got := activity[quiet]; got.Unread != 0 || got.Last != nil {
		t.Fatalf("quiet room = %+v", got)
	}
	if got, ok := activity["room_missing"]; !ok || got.Last != nil || got.Unread != 0 {
		t.Fatalf("a room with no teamspace = %+v, %v", got, ok)
	}

	for _, step := range []struct{ seq, want int64 }{{2, 2}, {1, 2}, {99, 3}} {
		read, err := fixture.store.MarkRead(ctx, busy, "user-ada", step.seq)
		if err != nil || read != step.want {
			t.Fatalf("mark read %d = %d, %v; want %d", step.seq, read, err, step.want)
		}
	}
	if read, err := fixture.store.MarkRead(ctx, quiet, "user-ada", 5); err != nil || read != 0 {
		t.Fatalf("mark an empty room read = %d, %v; want 0", read, err)
	}
	if _, err := fixture.store.MarkRead(ctx, "room_missing", "user-ada", 1); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("mark a room with no teamspace read: %v, want ErrRoomNotFound", err)
	}
	activity, err = fixture.store.Activity(ctx, fixture.organizationID, "user-ada", []string{busy})
	if err != nil || activity[busy].Unread != 0 || activity[busy].LastReadSeq != 3 {
		t.Fatalf("after reading everything = %+v, %v", activity[busy], err)
	}
	// Somebody else's cursor is their own.
	activity, err = fixture.store.Activity(ctx, fixture.organizationID, "user-bo", []string{busy})
	if err != nil || activity[busy].Unread != 2 {
		t.Fatalf("Bo's activity = %+v, %v; want Ada's message and her agent's unread", activity[busy], err)
	}

	if err := fixture.store.Forget(ctx, fixture.organizationID, "user-ada"); err != nil {
		t.Fatal(err)
	}
	var cursors int
	if err := fixture.database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.room_read_cursors
		WHERE organization_id=$1 AND user_id='user-ada'`, fixture.organizationID).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("Ada's cursors after Forget = %d, %v", cursors, err)
	}
}

// A parley's notes are one message per call, by the organization agent, kept
// with their data and read back with their kind; a message said is still
// kind message with no data, and a paused room takes no notes.
func TestParleyNotesArePostedOncePerCall(t *testing.T) {
	fixture := testStore(t)
	ctx := context.Background()
	room := fixture.newRoom(t)
	said, err := fixture.store.Append(ctx, Message{RoomID: room, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user-ada", Body: "let's call"})
	if err != nil || said.Kind != KindMessage || said.Data != nil {
		t.Fatalf("said = %+v, %v", said, err)
	}
	notes := ParleyNotes{CallID: "call_1", DurationMs: 60_000, Decisions: []string{"Ship"}, Actions: []ParleyAction{{Text: "Post it", OwnerUserID: "user-ada"}}}
	posted, created, err := fixture.store.AppendNotes(ctx, fixture.organizationID, room, notes, notesBody(notes))
	if err != nil || !created || posted.Seq != 2 || posted.Kind != KindParleyNotes || posted.AuthorKind != SeatAgent ||
		posted.AuthorUserID != "" || !strings.HasPrefix(posted.AuthorAgentID, "oag_") || posted.AuthorHandle != "rooms-test-agent" {
		t.Fatalf("posted = %+v, %v, %v", posted, created, err)
	}
	again, created, err := fixture.store.AppendNotes(ctx, fixture.organizationID, room, notes, notesBody(notes))
	if err != nil || created || again.ID != posted.ID {
		t.Fatalf("again = %+v, %v, %v", again, created, err)
	}
	// The repeat gave its number back: the next message is 3.
	next, err := fixture.store.Append(ctx, Message{RoomID: room, AuthorHandle: "ada", AuthorKind: SeatPerson, AuthorUserID: "user-ada", Body: "thanks"})
	if err != nil || next.Seq != 3 {
		t.Fatalf("next = %+v, %v", next, err)
	}

	messages, err := fixture.store.Messages(ctx, room, 0, 10)
	if err != nil || len(messages) != 3 || messages[1].Kind != KindParleyNotes {
		t.Fatalf("messages = %+v, %v", messages, err)
	}
	var data ParleyNotes
	if err := json.Unmarshal(messages[1].Data, &data); err != nil || data.CallID != "call_1" || data.Actions[0].OwnerUserID != "user-ada" {
		t.Fatalf("data = %s, %v", messages[1].Data, err)
	}
	activity, err := fixture.store.Activity(ctx, fixture.organizationID, "user-ada", []string{room})
	if err != nil || activity[room].Unread != 1 || activity[room].Last.Kind != KindMessage {
		t.Fatalf("activity = %+v, %v", activity[room], err)
	}

	if _, _, err := fixture.store.AppendNotes(ctx, "org_elsewhere", room, ParleyNotes{CallID: "call_2"}, "notes"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("notes for another organization's room: %v, want ErrRoomNotFound", err)
	}
	disabled := teamspaces.StatusDisabled
	if _, err := fixture.teamspaces.Update(ctx, fixture.organizationID, room, "user-ada", teamspaces.UpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.AppendNotes(ctx, fixture.organizationID, room, ParleyNotes{CallID: "call_2"}, "notes"); !errors.Is(err, ErrPaused) {
		t.Fatalf("notes for a paused room: %v, want ErrPaused", err)
	}
	// A member leaving takes their own messages, never the organization agent's
	// notes, but takes their id off the actions they owned.
	if err := fixture.store.Forget(ctx, fixture.organizationID, "user-ada"); err != nil {
		t.Fatal(err)
	}
	left, err := fixture.store.Messages(ctx, room, 0, 10)
	if err != nil || len(left) != 1 || left[0].Kind != KindParleyNotes {
		t.Fatalf("after Ada's Forget = %+v, %v", left, err)
	}
	var scrubbed ParleyNotes
	if err := json.Unmarshal(left[0].Data, &scrubbed); err != nil || len(scrubbed.Actions) != 1 ||
		scrubbed.Actions[0].Text != "Post it" || scrubbed.Actions[0].OwnerUserID != "" || strings.Contains(string(left[0].Data), "user-ada") {
		t.Fatalf("notes after Ada's Forget = %s, %v", left[0].Data, err)
	}
}
