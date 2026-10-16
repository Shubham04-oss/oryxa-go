package rooms

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// ---- fakes ----

// serviceFakeStore is an in-memory Store that remembers who was recorded as
// reading the room, which is what presence is made of.
type serviceFakeStore struct {
	mu       sync.Mutex
	messages []Message
	seq      int64
	brief    Brief
	watched  map[string]time.Time
	saved    []Brief
}

var _ Store = (*serviceFakeStore)(nil)

func newServiceFakeStore() *serviceFakeStore {
	return &serviceFakeStore{watched: map[string]time.Time{}}
}

func (store *serviceFakeStore) Append(_ context.Context, message Message) (Message, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.seq++
	message.Seq = store.seq
	message.ID = "rmg_" + strings.Repeat("x", int(store.seq))
	store.messages = append(store.messages, message)
	return message, nil
}

func (store *serviceFakeStore) Messages(_ context.Context, _ string, afterSeq int64, limit int) ([]Message, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var found []Message
	for _, message := range store.messages {
		if message.Seq > afterSeq && len(found) < limit {
			found = append(found, message)
		}
	}
	return found, nil
}

func (store *serviceFakeStore) Recent(_ context.Context, _ string, limit int) ([]Message, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.messages) <= limit {
		return slices.Clone(store.messages), nil
	}
	return slices.Clone(store.messages[len(store.messages)-limit:]), nil
}

func (store *serviceFakeStore) Brief(_ context.Context, _ string) (Brief, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.brief, nil
}

func (store *serviceFakeStore) SaveBrief(_ context.Context, brief Brief, _ int64) (Brief, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	brief.Revision = store.brief.Revision + 1
	store.brief = brief
	store.saved = append(store.saved, brief)
	return brief, nil
}

func (store *serviceFakeStore) Watch(_ context.Context, _, userID string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.watched[userID] = at
	return nil
}

func (store *serviceFakeStore) Watching(_ context.Context, _ string, since time.Time) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var here []string
	for userID, at := range store.watched {
		if at.After(since) {
			here = append(here, userID)
		}
	}
	slices.Sort(here)
	return here, nil
}

func (store *serviceFakeStore) Forget(_ context.Context, _, _ string) error { return nil }

// serviceFakeSpaces answers with one teamspace, whatever it is asked for.
type serviceFakeSpaces struct{ space teamspaces.Space }

func (spaces *serviceFakeSpaces) Get(_ context.Context, _ teamspaces.Actor, _ string) (teamspaces.Space, error) {
	return spaces.space, nil
}

// serviceFakeDecider answers with a fixed verdict and keeps what it was shown.
type serviceFakeDecider struct {
	verdict Verdict
	seen    []Situation
}

func (decider *serviceFakeDecider) Decide(_ context.Context, room Situation, _ Message) (Verdict, error) {
	decider.seen = append(decider.seen, room)
	return decider.verdict, nil
}

// serviceSilentSpeaker never has anything to add, so no agent turn lands in
// the room and a test only sees what the member said.
type serviceSilentSpeaker struct{}

func (serviceSilentSpeaker) Speak(context.Context, Seat, Situation, []Message) (string, error) {
	return Pass, nil
}

// serviceFakeCompactor counts what asked it to rewrite the brief.
type serviceFakeCompactor struct {
	calls int
	brief Brief
}

func (compactor *serviceFakeCompactor) Compact(context.Context, Situation, []Message) (Brief, error) {
	compactor.calls++
	return compactor.brief, nil
}

// aRoom is a teamspace with two people, each with their own agent, as
// teamspaces.Service shows it to a member of its team, which is who these
// tests act as.
func aRoom() teamspaces.Space {
	return teamspaces.Space{
		ID: "tsp_room", OrganizationID: "org_1", Name: "Launch Council",
		Status: teamspaces.StatusActive, IsMember: true,
		Whisperers: []teamspaces.Whisperer{
			{UserID: "user-ada", UserDisplayName: "Ada", AgentID: "agt_ada", AgentName: "Mira"},
			{UserID: "user-bo", UserDisplayName: "Bo", AgentID: "agt_bo", AgentName: "Cass"},
		},
		Contributions: []teamspaces.Contribution{{
			AgentID: "agt_ledger", OwnerUserID: "user-ada", Name: "Ledger",
			Role: "keeps the pricing sheet", Status: managedagents.Status("active"),
		}},
	}
}

func aService(t *testing.T, store Store, decider Decider, compactor Compactor) *Service {
	t.Helper()
	relay, err := NewRelay(store, serviceSilentSpeaker{})
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	service, err := New(Config{
		Store: store, Spaces: &serviceFakeSpaces{space: aRoom()},
		Decider: decider, Compactor: compactor, Relay: relay,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

// ---- tests ----

// Saying something is the plainest evidence there is that somebody is in the
// room. Before this was recorded, a member who typed but whose client had not
// polled recently was treated as away — and their own agent would answer over
// them, which is the one thing the seating is supposed to prevent.
func TestTalkingInARoomCountsAsBeingInIt(t *testing.T) {
	store := newServiceFakeStore()
	decider := &serviceFakeDecider{}
	service := aService(t, store, decider, nil)

	if _, err := service.Post(context.Background(), Actor{OrganizationID: "org_1", UserID: "user-ada"}, "tsp_room", "morning all"); err != nil {
		t.Fatalf("post: %v", err)
	}

	if len(decider.seen) != 1 {
		t.Fatalf("the decider was asked %d times, want once", len(decider.seen))
	}
	ada, found := personSeat(decider.seen[0].Seats, "user-ada")
	if !found {
		t.Fatal("the author has no seat in the room they just spoke in")
	}
	if !ada.Present {
		t.Error("the author was shown as away in the situation judged from their own message")
	}
	for _, seat := range decider.seen[0].Seats {
		if seat.Kind == SeatPerson && seat.UserID == "user-bo" && seat.Present {
			t.Error("somebody who has not read the room was shown as present")
		}
	}
}

// A member's message is stored addressing the seats it named, as an agent's
// answer is, so its event can say who was mentioned; an email address in it
// names nobody.
func TestAMembersMentionIsStoredWithTheirMessage(t *testing.T) {
	store := newServiceFakeStore()
	service := aService(t, store, &serviceFakeDecider{}, nil)

	said, err := service.Post(context.Background(), Actor{OrganizationID: "org_1", UserID: "user-ada"}, "tsp_room",
		"@bo and @ledger, the price list went to ops@northwind-2027.com")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(said) != 1 || !slices.Equal(said[0].AddressedTo, []string{"bo", "ledger"}) {
		t.Fatalf("said = %+v, want Ada's message addressing bo and ledger", said)
	}
	if stored := store.messages[0]; stored.AuthorHandle != "ada" || !slices.Equal(stored.AddressedTo, []string{"bo", "ledger"}) {
		t.Errorf("stored %+v, want it addressing bo and ledger", stored)
	}
}

// A room that has just concluded something has outgrown any brief that does
// not mention it, whatever the decider thought of the brief a moment earlier.
func TestConcludingSomethingRewritesTheBrief(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		verdict Verdict
		want    int
	}{
		{"a decision", Verdict{Keep: true, Kind: "decision"}, 1},
		{"a commitment", Verdict{Keep: true, Kind: "commitment"}, 1},
		{"the decider asking for it", Verdict{Compact: true}, 1},
		{"ordinary talk", Verdict{}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := newServiceFakeStore()
			compactor := &serviceFakeCompactor{brief: Brief{Summary: "where the room got to"}}
			service := aService(t, store, &serviceFakeDecider{verdict: testCase.verdict}, compactor)

			if _, err := service.Post(context.Background(), Actor{OrganizationID: "org_1", UserID: "user-ada"}, "tsp_room", "we go with Northwind at 4.50"); err != nil {
				t.Fatalf("post: %v", err)
			}

			if compactor.calls != testCase.want {
				t.Errorf("the brief was rewritten %d times, want %d", compactor.calls, testCase.want)
			}
		})
	}
}

// Reading a room answers with who is in it: a reader cannot address anybody
// without their handle, and cannot tell whether a colleague is here at all.
func TestReadingARoomSaysWhoIsInIt(t *testing.T) {
	store := newServiceFakeStore()
	service := aService(t, store, &serviceFakeDecider{}, nil)
	actor := Actor{OrganizationID: "org_1", UserID: "user-ada"}

	if _, err := service.Post(context.Background(), actor, "tsp_room", "morning all"); err != nil {
		t.Fatalf("post: %v", err)
	}
	said, seats, paused, err := service.Read(context.Background(), actor, "tsp_room", 0, 50)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(said) != 1 || paused {
		t.Fatalf("read %d messages, paused %t; want the one that was said, in a room that is not paused", len(said), paused)
	}
	ada, found := personSeat(seats, "user-ada")
	if !found || !ada.Present {
		t.Fatalf("the reader is not shown as present in the room they are reading: %+v", ada)
	}
	var specialist Seat
	for _, seat := range seats {
		if seat.AgentID == "agt_ledger" {
			specialist = seat
		}
	}
	if specialist.Handle != "ledger" || specialist.Role != "keeps the pricing sheet" {
		t.Errorf("the contributed specialist is not addressable from a read: %+v", specialist)
	}
}

// A paused teamspace's room stays the team's to read, its brief included,
// every read says it is paused, and nobody can say anything new in it.
func TestAPausedRoomIsReadOnly(t *testing.T) {
	store := newServiceFakeStore()
	service := aService(t, store, &serviceFakeDecider{}, nil)
	actor := Actor{OrganizationID: "org_1", UserID: "user-ada"}
	if _, err := service.Post(context.Background(), actor, "tsp_room", "before the pause"); err != nil {
		t.Fatalf("post: %v", err)
	}
	paused := aRoom()
	paused.Status = teamspaces.StatusDisabled
	service.spaces = &serviceFakeSpaces{space: paused}

	if _, err := service.Post(context.Background(), actor, "tsp_room", "after the pause"); !errors.Is(err, ErrPaused) {
		t.Fatalf("post in a paused room: %v, want ErrPaused", err)
	}
	said, _, readPaused, err := service.Read(context.Background(), actor, "tsp_room", 0, 50)
	if err != nil || len(said) != 1 || said[0].Body != "before the pause" || !readPaused {
		t.Fatalf("read a paused room = %+v, paused %t, %v; want what was said before it, paused", said, readPaused, err)
	}
	if _, missed, caughtUpPaused, err := service.Catchup(context.Background(), actor, "tsp_room", 0); err != nil || len(missed) != 1 || !caughtUpPaused {
		t.Fatalf("catch up on a paused room = %+v, paused %t, %v", missed, caughtUpPaused, err)
	}
}

// The ledger shows a conclusion by its first sentence, and a price is not
// the end of a sentence.
func TestAConclusionIsTitledBySentenceNotByFullStop(t *testing.T) {
	for _, testCase := range []struct{ body, want string }{
		{"we keep the enclosure at 4.50 and move on", "we keep the enclosure at 4.50 and move on"},
		{"Northwind confirmed. Shipping on the 14th.", "Northwind confirmed"},
		{"one line\nand another", "one line"},
		{strings.Repeat("holding the launch date ", 6), "holding the launch date holding the launch date holding the launch date…"},
	} {
		if got := firstLine(testCase.body); got != testCase.want {
			t.Errorf("firstLine(%q) = %q, want %q", testCase.body, got, testCase.want)
		}
	}
}
