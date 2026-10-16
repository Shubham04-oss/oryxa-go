package rooms

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// speakerTurn is one turn a speaker started, and who the room said was
// asking when it did.
type speakerTurn struct {
	member  threads.Member
	agentID string
	input   threads.RunInput
	profile managedagents.UnattendedProfile
	asker   agents.RoomAsker
	asking  bool
}

// speakerFakeRuns starts every turn it is asked to, recording it, and each
// turn says said.
type speakerFakeRuns struct {
	mu    sync.Mutex
	turns []speakerTurn
	said  string
}

func (runs *speakerFakeRuns) StartUnattendedRun(ctx context.Context, member threads.Member, id, _ string, input threads.RunInput, profile managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	asker, asking := agents.RoomAskerFrom(ctx)
	runs.mu.Lock()
	defer runs.mu.Unlock()
	runs.turns = append(runs.turns, speakerTurn{member: member, agentID: id, input: input, profile: profile, asker: asker, asking: asking})
	return managedagents.StartedRun{ThreadID: "thread_1", Run: speakerFakeRun{said: runs.said}}, nil
}

type speakerFakeRun struct{ said string }

func (speakerFakeRun) ID() string { return "run_1" }

func (run speakerFakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	_ = emit(threads.Item{Type: threads.ItemMessage, Author: "krude", Text: run.said})
	_ = emit(threads.Item{Type: threads.ItemRunCompleted})
}

func (speakerFakeRun) Discard(context.Context, string) {}

// speakerFakeThreads keeps which thread each seat uses in a room.
type speakerFakeThreads struct {
	mu      sync.Mutex
	threads map[string]string
}

func (store *speakerFakeThreads) Thread(_ context.Context, roomID, agentID string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.threads[roomID+"/"+agentID], nil
}

func (store *speakerFakeThreads) SetThread(_ context.Context, roomID, agentID, threadID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.threads == nil {
		store.threads = map[string]string{}
	}
	store.threads[roomID+"/"+agentID] = threadID
	return nil
}

// TestEveryTurnInARoomAnswersFromTheRoom: a seat's turn runs on the room's
// own profile whether its owner is reading the room or away, so it never
// reaches the member's apps, knowledge, memory or requests; only who the
// turn was for differs, beside its owner or standing in for them.
func TestEveryTurnInARoomAnswersFromTheRoom(t *testing.T) {
	mira := Seat{Handle: "mira", Kind: SeatAgent, UserID: "user-ada", AgentID: "agt_ada", SpeaksFor: "user-ada"}
	for _, present := range []bool{true, false} {
		runs := &speakerFakeRuns{said: "Northwind was agreed at 4.50."}
		speaker, err := NewAgentSpeaker(runs, &speakerFakeThreads{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		room := Situation{RoomID: "tsp_room", OrganizationID: "org_1", Name: "Launch Council", Seats: []Seat{
			{Handle: "ada", Kind: SeatPerson, UserID: "user-ada", Present: present}, mira,
		}}
		said, err := speaker.Speak(context.Background(), mira, room, []Message{{AuthorHandle: "bo", Body: "@mira what did we agree?"}})
		if err != nil || said != "Northwind was agreed at 4.50." {
			t.Fatalf("owner present %t: said %q, %v", present, said, err)
		}
		origin := agentsv1.StartedByTrigger
		if present {
			origin = agentsv1.StartedByTeam
		}
		if len(runs.turns) != 1 || runs.turns[0].profile != managedagents.RoomSeat || runs.turns[0].input.StartedBy != origin {
			t.Fatalf("owner present %t: turns %+v, want one on the room's profile, started by %s", present, runs.turns, origin)
		}
	}
}

// TestAPostSaysWhoIsAskingToTheTurnsItStarts: what a seat's turn cannot
// answer from the room it files with its own member, for the member whose
// message started the exchange: the room tells the turn who that is, with
// their own agent in the room, and the model never names them.
func TestAPostSaysWhoIsAskingToTheTurnsItStarts(t *testing.T) {
	runs := &speakerFakeRuns{said: "I have asked Ada."}
	speaker, err := NewAgentSpeaker(runs, &speakerFakeThreads{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := newServiceFakeStore()
	relay, err := NewRelay(store, speaker)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		Store: store, Spaces: &serviceFakeSpaces{space: aRoom()},
		Decider: &serviceFakeDecider{}, Relay: relay,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ada is away, so her own agent answers for her.
	if _, err := service.Post(context.Background(), Actor{OrganizationID: "org_1", UserID: "user-bo", AccessToken: "bo-token"}, "tsp_room", "@ada what does your CRM say about Northwind?"); err != nil {
		t.Fatal(err)
	}
	want := agents.RoomAsker{RoomID: "tsp_room", OrganizationID: "org_1", UserID: "user-bo", AgentID: "agt_bo", AgentName: "Cass", AgentKind: "primary"}
	if len(runs.turns) != 1 || runs.turns[0].agentID != "agt_ada" || !runs.turns[0].asking || runs.turns[0].asker != want {
		t.Fatalf("turns %+v, want Ada's agent's turn told that Bo, through Cass, is asking", runs.turns)
	}
}

// TestAnAdminOffTheTeamIsNotInItsRoom: an organization admin who is not on
// the team may manage its teamspace, but reads nothing said in its room,
// catches up on none of it, and says nothing there; nothing records them as
// having been in it either.
func TestAnAdminOffTheTeamIsNotInItsRoom(t *testing.T) {
	space := aRoom()
	space.IsMember, space.CanManage = false, true
	store := newServiceFakeStore()
	relay, err := NewRelay(store, serviceSilentSpeaker{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Store: store, Spaces: &serviceFakeSpaces{space: space}, Decider: &serviceFakeDecider{}, Relay: relay})
	if err != nil {
		t.Fatal(err)
	}
	admin := Actor{OrganizationID: "org_1", UserID: "user-admin", AccessToken: "admin-token"}
	if _, _, _, err := service.Read(context.Background(), admin, "tsp_room", 0, 50); !errors.Is(err, ErrForbidden) {
		t.Errorf("reading the room: error=%v, want ErrForbidden", err)
	}
	if _, _, _, err := service.Catchup(context.Background(), admin, "tsp_room", 0); !errors.Is(err, ErrForbidden) {
		t.Errorf("catching up on the room: error=%v, want ErrForbidden", err)
	}
	if _, err := service.Post(context.Background(), admin, "tsp_room", "what is everyone saying?"); !errors.Is(err, ErrForbidden) {
		t.Errorf("posting in the room: error=%v, want ErrForbidden", err)
	}
	if len(store.messages) != 0 || len(store.watched) != 0 {
		t.Errorf("stored %v and watched %v, want nothing", store.messages, store.watched)
	}
}

// guardFinding answers each text as found says, keeping every text it read.
type guardFinding struct {
	mu    sync.Mutex
	found map[string]answerguard.Verdict
	read  []string
}

func (guard *guardFinding) Check(_ context.Context, texts []string) answerguard.Verdict {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.read = append(guard.read, texts...)
	if verdict, ok := guard.found[texts[0]]; ok {
		return verdict
	}
	return answerguard.Verdict{State: answerguard.StateClear, Texts: slices.Clone(texts)}
}

// TestTheGuardReadsWhatAnAgentSaysInARoom: the whole team reads a room, so
// every agent's answer is checked before it is stored: said as it was when
// nothing is found, masked where the guard masks, with every held value
// replaced by its kind, since nobody reviews a room's answers before the
// team reads them, and not said at all when it could not be checked. What
// people say is theirs, and is not checked.
func TestTheGuardReadsWhatAnAgentSaysInARoom(t *testing.T) {
	const (
		plain   = "Northwind was agreed at 4.50."
		secret  = "The staging password is hunter2."
		contact = "Reach Priya at priya@example.com."
		broken  = "Anything at all."
	)
	guard := &guardFinding{found: map[string]answerguard.Verdict{
		secret:  {State: answerguard.StateMasked, Kinds: []string{"PASSWORD"}, Texts: []string{"The staging password is {PASSWORD}."}},
		contact: {State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{contact}, Masked: []string{"Reach Priya at {EMAIL}."}},
		broken:  {State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: []string{broken}},
	}}
	bo := relayPersonSeat("bo", "user-bo", true)
	for said, heard := range map[string]string{
		plain:   plain,
		secret:  "The staging password is {PASSWORD}.",
		contact: "Reach Priya at {EMAIL}.",
		broken:  unchecked,
	} {
		ada := relayAgentSeat("mira", "user-ada", "agt_ada")
		store := &relayFakeStore{}
		speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) { return said, nil }}
		relay := &Relay{Store: store, Speaker: speaker, Guard: guard}
		results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, ada}}, relayMessage("room-1", bo, "@mira what did we agree?"), Verdict{})
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 || results[1].Body != heard || store.messages[1].Body != heard {
			t.Errorf("mira said %q; the room heard %+v, want %q", said, results, heard)
		}
	}
	if slices.Contains(guard.read, "@mira what did we agree?") {
		t.Error("the guard read what a person said")
	}

	store := &relayFakeStore{}
	unguarded := &Relay{Store: store, Speaker: &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) { return contact, nil }}}
	if results, err := unguarded.Post(context.Background(), Situation{Seats: []Seat{bo, relayAgentSeat("mira", "user-ada", "agt_ada")}}, relayMessage("room-1", bo, "@mira?"), Verdict{}); err != nil || len(results) != 2 || results[1].Body != contact {
		t.Fatalf("without a guard the room heard %+v, %v; want the answer as said", results, err)
	}
}
