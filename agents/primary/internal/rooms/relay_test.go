package rooms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// ---- fakes ----
//
// Named with a relay prefix on purpose: other files land in this same
// package while this one is being written, and a generic fakeStore or
// fakeSpeaker is exactly the name another test in the package might also
// reach for.

// relayFakeStore is an in-memory Store. Append is where sequence numbers are
// assigned, which is the only behaviour the relay actually depends on.
type relayFakeStore struct {
	mu       sync.Mutex
	messages []Message
	seq      int64
}

var _ Store = (*relayFakeStore)(nil)

func (s *relayFakeStore) Append(_ context.Context, message Message) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	message.Seq = s.seq
	if message.ID == "" {
		message.ID = fmt.Sprintf("msg-%d", message.Seq)
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Unix(message.Seq, 0).UTC()
	}
	s.messages = append(s.messages, message)
	return message, nil
}

func (s *relayFakeStore) Messages(_ context.Context, roomID string, afterSeq int64, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, message := range s.messages {
		if message.RoomID != roomID || message.Seq <= afterSeq {
			continue
		}
		out = append(out, message)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *relayFakeStore) Recent(_ context.Context, roomID string, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, message := range s.messages {
		if message.RoomID == roomID {
			out = append(out, message)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *relayFakeStore) Brief(_ context.Context, roomID string) (Brief, error) {
	return Brief{RoomID: roomID}, nil
}

func (s *relayFakeStore) SaveBrief(_ context.Context, brief Brief, _ int64) (Brief, error) {
	return brief, nil
}

func (s *relayFakeStore) Watch(_ context.Context, _, _ string, _ time.Time) error { return nil }

func (s *relayFakeStore) Watching(_ context.Context, _ string, _ time.Time) ([]string, error) {
	return nil, nil
}

func (s *relayFakeStore) Forget(_ context.Context, _, _ string) error { return nil }

func (s *relayFakeStore) MarkRead(context.Context, string, string, int64) (int64, error) {
	return 0, nil
}

func (s *relayFakeStore) Activity(context.Context, string, string, []string) (map[string]Activity, error) {
	return map[string]Activity{}, nil
}

// relayFailingAppendStore fails every Append, to prove Post reports it.
type relayFailingAppendStore struct {
	relayFakeStore
	err error
}

func (s *relayFailingAppendStore) Append(context.Context, Message) (Message, error) {
	return Message{}, s.err
}

// relayPausingStore is a room a manager pauses partway through: once paused
// is set, every Append is refused as the store refuses one into a paused
// teamspace.
type relayPausingStore struct {
	relayFakeStore
	paused bool
}

func (s *relayPausingStore) pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
}

func (s *relayPausingStore) Append(ctx context.Context, message Message) (Message, error) {
	s.mu.Lock()
	paused := s.paused
	s.mu.Unlock()
	if paused {
		return Message{}, ErrPaused
	}
	return s.relayFakeStore.Append(ctx, message)
}

// relaySpeakCall records one Speak invocation for a test to inspect.
type relaySpeakCall struct {
	seat       Seat
	room       Situation
	transcript []Message
}

// relayFakeSpeaker is a Speaker whose answer for each call is computed by
// reply. A nil reply means every seat passes, which is the quietest fake and
// the safest default for tests that do not care.
type relayFakeSpeaker struct {
	mu    sync.Mutex
	calls []relaySpeakCall
	reply func(seat Seat, room Situation, transcript []Message) (string, error)
}

var _ Speaker = (*relayFakeSpeaker)(nil)

func (s *relayFakeSpeaker) Speak(_ context.Context, seat Seat, room Situation, transcript []Message) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, relaySpeakCall{seat: seat, room: room, transcript: transcript})
	s.mu.Unlock()
	if s.reply == nil {
		return Pass, nil
	}
	return s.reply(seat, room, transcript)
}

func (s *relayFakeSpeaker) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ---- fixtures ----

func relayPersonSeat(handle, userID string, present bool) Seat {
	return Seat{Handle: handle, Kind: SeatPerson, UserID: userID, DisplayName: handle, Present: present}
}

func relayAgentSeat(handle, userID, agentID string) Seat {
	return Seat{Handle: handle, Kind: SeatAgent, UserID: userID, AgentID: agentID, AgentName: handle, DisplayName: handle}
}

func relayMessage(roomID string, author Seat, body string) Message {
	return Message{
		RoomID: roomID, AuthorHandle: author.Handle, AuthorKind: author.Kind,
		AuthorUserID: author.UserID, AuthorAgentID: author.AgentID,
		Body: body, CreatedAt: time.Now().UTC(),
	}
}

// ---- tests ----

func TestRelayPost_AddressedAgentAnswers(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "ada" {
			return "On it.", nil
		}
		return Pass, nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Name: "room", Seats: []Seat{bob, ada}}
	msg := relayMessage("room-1", bob, "@ada please check this")

	results, err := relay.Post(context.Background(), room, msg, Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d messages, want 2 (post + ada's reply): %+v", len(results), results)
	}
	if results[1].AuthorHandle != "ada" || results[1].Body != "On it." {
		t.Errorf("reply = %+v, want ada saying %q", results[1], "On it.")
	}
	if results[1].InReplyToSeq != results[0].Seq {
		t.Errorf("InReplyToSeq = %d, want %d", results[1].InReplyToSeq, results[0].Seq)
	}
	if got := speaker.callCount(); got != 1 {
		t.Errorf("Speak called %d times, want 1", got)
	}
}

// A room paused while an agent is taking its turn stores nothing from then
// on: not the answer, and not the turn that answer would have woken.
func TestRelayPost_ARoomPausedDuringATurnTakesNothingMore(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	cara := relayAgentSeat("cara", "user-cara", "agent-cara")
	store := &relayPausingStore{}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "ada" {
			store.pause()
			return "@cara over to you.", nil
		}
		return "Here.", nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, ada, cara}}

	results, err := relay.Post(context.Background(), room, relayMessage("room-1", bob, "@ada what's left?"), Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 1 || results[0].AuthorHandle != "bob" {
		t.Fatalf("got %+v, want only what bob said before the pause", results)
	}
	if got := len(store.messages); got != 1 {
		t.Errorf("the store holds %d messages, want 1", got)
	}
	if got := speaker.callCount(); got != 1 {
		t.Errorf("Speak called %d times, want 1: cara was named only by an answer the paused room refused", got)
	}
}

// A room paused before the message itself is stored refuses it as paused.
func TestRelayPost_APausedRoomRefusesThePost(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	store := &relayPausingStore{paused: true}
	relay := &Relay{Store: store, Speaker: &relayFakeSpeaker{}}

	if _, err := relay.Post(context.Background(), Situation{Seats: []Seat{bob}}, relayMessage("room-1", bob, "hello"), Verdict{}); !errors.Is(err, ErrPaused) {
		t.Fatalf("Post into a paused room: %v, want ErrPaused", err)
	}
}

func TestRelayPost_AddressedPresentPersonMeansNothingRuns(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	carol := relayPersonSeat("carol", "user-carol", true) // present
	carolAgent := relayAgentSeat("carol-agent", "user-carol", "agent-carol")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, carol, carolAgent}}
	msg := relayMessage("room-1", bob, "@carol can you take this?")

	// The verdict would route to carol's own agent if it got a say. It must
	// not: carol is present and was addressed directly, so rule 1 settles
	// this before the verdict is ever consulted.
	results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: true, Speaker: "carol-agent"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d messages, want 1 (just the post): %+v", len(results), results)
	}
	if got := speaker.callCount(); got != 0 {
		t.Errorf("Speak called %d times, want 0 — carol answers for herself", got)
	}
}

func TestRelayPost_AddressedAbsentPersonRunsTheirAgent(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	dan := relayPersonSeat("dan", "user-dan", false) // away
	danAgent := relayAgentSeat("dan-agent", "user-dan", "agent-dan")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "dan-agent" {
			return "Dan's away; his agent says: yes, it's ready.", nil
		}
		return Pass, nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, dan, danAgent}}
	msg := relayMessage("room-1", bob, "@dan is this ready?")

	results, err := relay.Post(context.Background(), room, msg, Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 2 || results[1].AuthorHandle != "dan-agent" {
		t.Fatalf("got %+v, want dan's own agent standing in for him", results)
	}
}

func TestRelayPost_NobodyAddressedDeciderSaysQuiet(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, ada}}
	msg := relayMessage("room-1", bob, "just thinking out loud, nothing urgent")

	results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: false})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d messages, want 1 (just the post): %+v", len(results), results)
	}
	if got := speaker.callCount(); got != 0 {
		t.Errorf("Speak called %d times, want 0", got)
	}
}

func TestRelayPost_NobodyAddressedDeciderRoutesToSeat(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		return "happy to help", nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, ada}}
	msg := relayMessage("room-1", bob, "does anybody know the status here?")

	results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: true, Speaker: "ada"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 2 || results[1].AuthorHandle != "ada" {
		t.Fatalf("got %+v, want ada routed to by the decider's verdict", results)
	}
}

func TestRelayPost_RoutedSeatIgnoredUnlessAgentInThisRoom(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	carol := relayPersonSeat("carol", "user-carol", true) // a person, not an agent
	seats := []Seat{bob, carol}

	cases := []struct {
		name    string
		speaker string
	}{
		{"handle does not exist in this room", "nobody-here"},
		{"handle exists but is a person, not an agent", "carol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &relayFakeStore{}
			speaker := &relayFakeSpeaker{}
			relay := &Relay{Store: store, Speaker: speaker}
			room := Situation{Seats: seats}
			msg := relayMessage("room-1", bob, "anybody around?")

			results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: true, Speaker: tc.speaker})
			if err != nil {
				t.Fatalf("Post: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("got %d messages, want 1 (just the post): %+v", len(results), results)
			}
			if got := speaker.callCount(); got != 0 {
				t.Errorf("Speak called %d times, want 0", got)
			}
		})
	}
}

func TestRelayPost_PassEndsRelayQuietly(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		return Pass, nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, ada}}
	msg := relayMessage("room-1", bob, "@ada anything to add?")

	results, err := relay.Post(context.Background(), room, msg, Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d messages, want 1 — PASS is not appended: %+v", len(results), results)
	}
	if got := speaker.callCount(); got != 1 {
		t.Errorf("Speak called %d times, want exactly 1 (never retried)", got)
	}
}

// TestRelayPost_TwoAgentsAlwaysSpeaking_TerminatesWithinTurnBudget is the
// termination proof rule 4 asks for: two agents that always address one
// another back and forth, which without a turn budget would never stop.
func TestRelayPost_TwoAgentsAlwaysSpeaking_TerminatesWithinTurnBudget(t *testing.T) {
	carol := relayPersonSeat("carol", "user-carol", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	bilbo := relayAgentSeat("bilbo", "user-bilbo", "agent-bilbo")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{}
	speaker.reply = func(seat Seat, _ Situation, _ []Message) (string, error) {
		switch seat.Handle {
		case "ada":
			return "Interesting — @bilbo, what do you think?", nil
		case "bilbo":
			return "Agreed — @ada, let's keep going.", nil
		default:
			return Pass, nil
		}
	}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{carol, ada, bilbo}}
	msg := relayMessage("room-1", carol, "kick us off please")

	type outcome struct {
		results []Message
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: true, Speaker: "ada"})
		done <- outcome{results, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Post: %v", got.err)
		}
		if callCount := speaker.callCount(); callCount != maxAgentTurns {
			t.Errorf("Speak called %d times, want exactly maxAgentTurns=%d", callCount, maxAgentTurns)
		}
		if len(got.results) != 1+maxAgentTurns {
			t.Errorf("got %d messages, want %d (the post plus one per turn)", len(got.results), 1+maxAgentTurns)
		}
		for i := 1; i < len(got.results); i++ {
			if got.results[i].AuthorHandle == got.results[i-1].AuthorHandle {
				t.Errorf("message %d answered by %s, same as the one it replies to", i, got.results[i].AuthorHandle)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Post did not return; two always-speaking agents produced an unbounded relay")
	}
}

func TestRelayPost_SpeakerErrorDoesNotStopOthers(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	a1 := relayAgentSeat("a1", "user-1", "agent-1")
	a2 := relayAgentSeat("a2", "user-2", "agent-2")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "a1" {
			return "", errors.New("a1's agent is down")
		}
		return "all good here", nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, a1, a2}}
	msg := relayMessage("room-1", bob, "@a1 @a2 can you both check?")

	results, err := relay.Post(context.Background(), room, msg, Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d messages, want 3 (post, a1's line saying it could not answer, a2's reply): %+v", len(results), results)
	}
	if results[1].AuthorHandle != "a1" || results[1].Body != "@a1 couldn't answer right now." {
		t.Errorf("first line = %+v, want a1 saying it could not answer", results[1])
	}
	if results[2].AuthorHandle != "a2" || results[2].Body != "all good here" {
		t.Errorf("reply = %+v, want a2's answer", results[2])
	}
	if got := speaker.callCount(); got != 2 {
		t.Errorf("Speak called %d times, want 2 — both a1 and a2 were tried", got)
	}
}

func TestRelayPost_AuthorNeverWokenByOwnMention(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	bobAgent := relayAgentSeat("bob-agent", "user-bob", "agent-bob")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		return "logged it", nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, bobAgent}}
	// The only "@" in the body names the author's own handle.
	msg := relayMessage("room-1", bob, "as @bob said earlier, let's keep going")

	// The verdict routes to bob's own agent. If the self-mention were
	// mistaken for bob addressing himself, rule 1 (a present person answers
	// for themselves) would end the relay right here and bob-agent would
	// never run.
	results, err := relay.Post(context.Background(), room, msg, Verdict{Answer: true, Speaker: "bob-agent"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 2 || results[1].AuthorHandle != "bob-agent" {
		t.Fatalf("got %+v, want bob-agent answering via the verdict, proving the self-mention did not address anybody", results)
	}
}

// TestRelayPost_TranscriptIsWindowPlusTrigger checks rule 5's rendering
// contract directly: a Speaker sees the brief (carried untouched in room),
// the last transcriptWindow messages, and always the message that woke it.
func TestRelayPost_TranscriptIsWindowPlusTrigger(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	store := &relayFakeStore{}

	recent := make([]Message, 0, transcriptWindow+5)
	for i := 0; i < transcriptWindow+5; i++ {
		recent = append(recent, Message{
			RoomID: "room-1", Seq: int64(i + 1), AuthorHandle: "bob", AuthorKind: SeatPerson,
			AuthorUserID: "user-bob", Body: fmt.Sprintf("message %d", i), CreatedAt: time.Now().UTC(),
		})
	}

	var gotTranscript []Message
	var gotBrief Brief
	speaker := &relayFakeSpeaker{reply: func(_ Seat, room Situation, transcript []Message) (string, error) {
		gotTranscript = transcript
		gotBrief = room.Brief
		return "noted", nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{
		Seats:  []Seat{bob, ada},
		Recent: recent,
		Brief:  Brief{RoomID: "room-1", Summary: "stands in for everything older"},
	}
	msg := relayMessage("room-1", bob, "@ada latest update please")

	if _, err := relay.Post(context.Background(), room, msg, Verdict{}); err != nil {
		t.Fatalf("Post: %v", err)
	}

	if len(gotTranscript) != transcriptWindow+1 {
		t.Fatalf("transcript has %d messages, want transcriptWindow+1=%d", len(gotTranscript), transcriptWindow+1)
	}
	wantTail := recent[len(recent)-transcriptWindow:]
	for i, want := range wantTail {
		if gotTranscript[i].Body != want.Body {
			t.Errorf("transcript[%d] = %q, want %q (the most recent %d, not the oldest)",
				i, gotTranscript[i].Body, want.Body, transcriptWindow)
		}
	}
	if last := gotTranscript[len(gotTranscript)-1]; last.Body != msg.Body {
		t.Errorf("last transcript entry = %q, want the triggering message %q", last.Body, msg.Body)
	}
	if gotBrief.Summary != room.Brief.Summary {
		t.Errorf("brief handed to the speaker = %+v, want the room's own brief untouched", gotBrief)
	}
}

func TestRelayPost_AppendFailureIsReported(t *testing.T) {
	wantErr := errors.New("db is down")
	store := &relayFailingAppendStore{err: wantErr}
	relay := &Relay{Store: store, Speaker: &relayFakeSpeaker{}}

	_, err := relay.Post(context.Background(), Situation{}, Message{RoomID: "room-1"}, Verdict{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestNewRelay(t *testing.T) {
	if _, err := NewRelay(nil, &relayFakeSpeaker{}); err == nil {
		t.Error("NewRelay with a nil store: want an error")
	}
	if _, err := NewRelay(&relayFakeStore{}, nil); err == nil {
		t.Error("NewRelay with a nil speaker: want an error")
	}
	relay, err := NewRelay(&relayFakeStore{}, &relayFakeSpeaker{})
	if err != nil || relay == nil {
		t.Fatalf("NewRelay(store, speaker) = (%v, %v), want a relay and no error", relay, err)
	}
}

// TestRelayPost_StoresOnlyTheSeatsAMessageNamed: a message, a person's or an
// agent's, is stored addressing the room's seats it named, and nothing else
// that followed an "@": not an email address's domain, not a handle nobody
// here has, and not a list the caller brought along.
func TestRelayPost_StoresOnlyTheSeatsAMessageNamed(t *testing.T) {
	bob := relayPersonSeat("bob", "user-bob", true)
	ada := relayAgentSeat("ada", "user-ada", "agent-ada")
	cy := relayAgentSeat("cy", "user-cy", "agent-cy")
	store := &relayFakeStore{}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		switch seat.Handle {
		case "ada":
			return "Sent to ops@acme-merger-2027.com. @bob, @dana has it too; @cy, your turn.", nil
		case "cy":
			return "Filed it under billing@acme-merger-2027.com.", nil
		}
		return Pass, nil
	}}
	relay := &Relay{Store: store, Speaker: speaker}
	room := Situation{Seats: []Seat{bob, ada, cy}}
	msg := relayMessage("room-1", bob, "@ada mail the deck to ops@acme-merger-2027.com, cc @nobody-here and @ada again")
	msg.AddressedTo = []string{"acme-merger-2027"}

	results, err := relay.Post(context.Background(), room, msg, Verdict{})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if len(results) != 3 || results[1].AuthorHandle != "ada" || results[2].AuthorHandle != "cy" {
		t.Fatalf("got %+v, want bob's message, ada's answer and cy's", results)
	}
	for index, want := range [][]string{{"ada"}, {"bob", "cy"}, nil} {
		if got := results[index].AddressedTo; !slices.Equal(got, want) {
			t.Errorf("message %d addresses %v, want %v", index, got, want)
		}
		if stored := store.messages[index].AddressedTo; !slices.Equal(stored, want) {
			t.Errorf("message %d is stored addressing %v, want %v", index, stored, want)
		}
	}
}

func TestSeatHandles(t *testing.T) {
	seats := []Seat{relayPersonSeat("bob", "user-bob", true), relayAgentSeat("ada", "user-ada", "agent-ada")}
	for _, test := range []struct {
		body, author string
		want         []string
	}{
		{"@ada and @bob, and @ada again", "carol", []string{"ada", "bob"}},
		{"write to ops@acme-merger-2027.com", "bob", nil},
		{"@bob talking about himself to @ada", "bob", []string{"ada"}},
		{"no mentions here", "bob", nil},
	} {
		if got := seatHandles(seats, test.body, test.author); !slices.Equal(got, test.want) {
			t.Errorf("seatHandles(%q, author %s) = %v, want %v", test.body, test.author, got, test.want)
		}
	}
}

func TestAddressedHandles(t *testing.T) {
	got := addressedHandles("@ada, @bob and @ada again, but not @carol's own mention", "carol")
	want := []string{"ada", "bob"}
	if !slices.Equal(got, want) {
		t.Errorf("addressedHandles = %v, want %v", got, want)
	}

	if got := addressedHandles("no mentions here", "carol"); len(got) != 0 {
		t.Errorf("addressedHandles with no mentions = %v, want none", got)
	}

	if got := addressedHandles("@carol talking about herself", "carol"); len(got) != 0 {
		t.Errorf("addressedHandles naming only the author = %v, want none", got)
	}
}
